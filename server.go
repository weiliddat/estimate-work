package main

import (
	"bytes"
	"embed"
	"encoding/gob"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	//go:embed templates/*.html
	templatesFs  embed.FS
	funcs        = template.FuncMap{"join": strings.Join}
	indexTmpl    = template.Must(template.New("index").Funcs(funcs).ParseFS(templatesFs, "templates/base.html", "templates/index.html"))
	roomTmpl     = template.Must(template.New("room").Funcs(funcs).ParseFS(templatesFs, "templates/base.html", "templates/room.html"))
	notFoundTmpl = template.Must(template.New("notFound").Funcs(funcs).ParseFS(templatesFs, "templates/base.html", "templates/not_found.html"))
	rooms        = make(map[string]*Room)
)

type User struct {
	Id   string
	Name string
}

type Room struct {
	Id   string
	Name string

	HostId string
	Users  []*User

	Topic     string
	Options   []string
	Estimates map[string]string
	Revealed  bool

	UpdatedAt time.Time
	mu        sync.Mutex
	subs      [](chan bool)
}

func (r *Room) GetUser(id string) *User {
	for _, u := range r.Users {
		if u.Id == id {
			return u
		}
	}

	return nil
}

func (r *Room) DisplayName() string {
	if r.Name != "" {
		return r.Name
	}

	return r.Id
}

func NewRoom() *Room {
	id, _ := uuid.NewV7()
	room := Room{
		Id:        id.String(),
		Users:     []*User{},
		UpdatedAt: time.Now(),
		Options:   []string{"🤷", "0", "1", "2", "3", "5", "8", "13", "21", "🤯"},
		Estimates: make(map[string]string),
	}
	rooms[room.Id] = &room
	return &room
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	room, user := getReqRoomUser(r)

	err := indexTmpl.ExecuteTemplate(
		w,
		"base",
		struct {
			User *User
			Room *Room
		}{
			User: user,
			Room: room,
		},
	)
	if err != nil {
		internalErrorHandler(w, r, err)
	}
}

func getReqRoomUser(r *http.Request) (*Room, *User) {
	var roomId, userId string

	roomId = r.PathValue("room")
	if roomId == "" {
		roomCookie, err := r.Cookie("room")
		if err == nil {
			roomId = roomCookie.Value
		}
	}

	room, exists := rooms[roomId]
	if !exists {
		return nil, nil
	}

	userCookie, err := r.Cookie("user")
	if err != nil {
		return room, nil
	}
	userId = userCookie.Value

	user := room.GetUser(userId)

	return room, user
}

func getRoomHandler(w http.ResponseWriter, r *http.Request) {
	room, user := getReqRoomUser(r)

	if room == nil {
		notFoundHandler(w, r)
		return
	}

	err := roomTmpl.ExecuteTemplate(
		w,
		"base",
		struct {
			User *User
			Room *Room
		}{
			User: user,
			Room: room,
		},
	)
	if err != nil {
		internalErrorHandler(w, r, err)
	}
}

func getRoomUpdateHandler(w http.ResponseWriter, r *http.Request) {
	room, user := getReqRoomUser(r)

	// Redirect user to homepage if kicked
	if room == nil || user == nil {
		if r.Header.Get("hx-request") == "true" {
			w.Header().Add("hx-location", "/")
			return
		} else {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
	}

	// Long polling
	hasUpdates := true
	ifModifiedSince := r.Header.Get("If-Modified-Since")
	ifModifiedSinceTime, err := time.Parse(time.RFC1123, ifModifiedSince)
	if err == nil && !room.UpdatedAt.Truncate(time.Second).After(ifModifiedSinceTime) {
		roomUpdates := make(chan bool)

		room.mu.Lock()
		room.subs = append(room.subs, roomUpdates)
		room.mu.Unlock()

		select {
		case <-r.Context().Done():
		case <-roomUpdates:
		case <-time.After(20 * time.Second):
			hasUpdates = false
		}

		room.mu.Lock()
		room.subs = slices.DeleteFunc(
			room.subs,
			func(s chan bool) bool { return s == roomUpdates },
		)
		room.mu.Unlock()
		close(roomUpdates)
	}

	w.Header().Add("Last-Modified", room.UpdatedAt.Format(time.RFC1123))
	w.Header().Add("Cache-Control", "no-cache")

	if !hasUpdates {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	// Partial template if Hx-Request
	templateName := "base"
	if r.Header.Get("hx-request") == "true" {
		templateName = "updates-only"
	}

	err = roomTmpl.ExecuteTemplate(
		w,
		templateName,
		struct {
			User *User
			Room *Room
		}{
			User: user,
			Room: room,
		},
	)
	if err != nil {
		internalErrorHandler(w, r, err)
	}
}

func createRoomHandler(w http.ResponseWriter, r *http.Request) {
	room := NewRoom()
	rooms[room.Id] = room

	http.Redirect(w, r, fmt.Sprintf("/room/%s", room.Id), http.StatusSeeOther)
}

func updateRoomHandler(w http.ResponseWriter, r *http.Request) {
	room, user := getReqRoomUser(r)

	if room == nil {
		notFoundHandler(w, r)
		return
	}

	room.mu.Lock()
	defer room.mu.Unlock()

	newUserName := r.FormValue("user-name")
	if newUserName != "" {
		if user != nil {
			// User exists, just update name
			user.Name = newUserName
		} else {
			// Create user and join room (as host if doesn't exist)
			var userId string

			// Reuse userId to rejoin other rooms
			userCookie, err := r.Cookie("user")
			if err == nil && userCookie != nil {
				userId = userCookie.Value
			} else {
				id, _ := uuid.NewV7()
				userId = id.String()
			}

			user = &User{
				Id:   userId,
				Name: newUserName,
			}

			room.Users = append(room.Users, user)
			if room.HostId == "" {
				room.HostId = user.Id
			}

			http.SetCookie(w, &http.Cookie{
				Name:  "room",
				Value: room.Id,
				Path:  "/",
			})
			http.SetCookie(w, &http.Cookie{
				Name:  "user",
				Value: user.Id,
				Path:  "/",
			})
		}
	}

	newRoomName := r.FormValue("name")
	if newRoomName != "" {
		room.Name = newRoomName
	}

	newRoomTopic := r.FormValue("topic")
	if newRoomTopic != "" {
		room.Topic = newRoomTopic
	}

	newEstimate := r.FormValue("estimate")
	if newEstimate != "" {
		room, user := getReqRoomUser(r)

		existingEstimate, exists := room.Estimates[user.Id]
		if exists && existingEstimate == newEstimate {
			delete(room.Estimates, user.Id)
		} else {
			room.Estimates[user.Id] = newEstimate
		}
	}

	newRevealed := r.FormValue("reveal")
	if newRevealed != "" {
		room.Revealed = newRevealed == "true"
	}

	newOptions := r.FormValue("options")
	if newOptions != "" {
		room.Options = []string{}
		for _, v := range strings.Split(newOptions, ",") {
			room.Options = append(room.Options, strings.TrimSpace(v))
		}
	}

	kickUsers := r.FormValue("kick")
	if kickUsers == "true" {
		room.Users = []*User{}
		room.Estimates = make(map[string]string)
	}

	room.UpdatedAt = time.Now()
	for _, sub := range room.subs {
		sub <- true
	}

	hxRequest := r.Header.Get("hx-request") == "true"
	if hxRequest {
		http.Redirect(w, r, fmt.Sprintf("/room/%s/update", room.Id), http.StatusSeeOther)
	} else {
		http.Redirect(w, r, fmt.Sprintf("/room/%s", room.Id), http.StatusSeeOther)
	}
}

func notFoundHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("hx-refresh", "true")
	w.WriteHeader(http.StatusNotFound)
	err := notFoundTmpl.ExecuteTemplate(w, "base", nil)
	if err != nil {
		internalErrorHandler(w, r, err)
	}
}

func internalErrorHandler(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("Error %+v caused by %+v\n", err, r)
	w.Header().Add("hx-refresh", "true")
	w.WriteHeader(http.StatusInternalServerError)
	io.WriteString(w, "Internal Server Error")
}

func readFromCacheFile() {
	cacheFilePath := os.Getenv("CACHE_FILE_PATH")
	cacheFile, err := os.ReadFile(cacheFilePath)
	if os.IsNotExist(err) {
		log.Println("Cache file does not exist, OK")
	} else if err != nil {
		log.Fatal("Failed to read file: ", err)
	} else {
		cacheBytes := bytes.NewBuffer(cacheFile)
		cacheDecoder := gob.NewDecoder(cacheBytes)
		err = cacheDecoder.Decode(&rooms)
		if err != nil {
			log.Fatal("Failed to serialize from file: ", err)
		}
		log.Printf("Restored from cache file %v rooms", len(rooms))
	}
}

func writeToCacheFile() {
	for _, r := range rooms {
		r.mu.Lock()
	}
	cacheFilePath := os.Getenv("CACHE_FILE_PATH")
	cacheFile, err := os.Create(cacheFilePath)
	if err != nil {
		log.Fatal("Failed to create file: ", err)
	}
	cacheEncoder := gob.NewEncoder(cacheFile)
	err = cacheEncoder.Encode(rooms)
	if err != nil {
		log.Fatal("Failed to serialize to file: ", err)
	}
	for _, r := range rooms {
		r.mu.Unlock()
	}
}

func cleanupOldRooms() {
	tenDaysAgo := time.Now().Add(-10 * 24 * time.Hour)
	for _, r := range rooms {
		r.mu.Lock()
		if r.UpdatedAt.Before(tenDaysAgo) {
			log.Printf("Cleaning up room %+v", r)
			delete(rooms, r.Id)
		} else {
			r.mu.Unlock()
		}
	}
}

func main() {
	listenAddr := os.Getenv("LISTEN")

	gob.Register(Room{})
	readFromCacheFile()
	writeInterval := time.NewTicker(1 * time.Second)
	go func() {
		for {
			<-writeInterval.C
			cleanupOldRooms()
			writeToCacheFile()
		}
	}()

	http.HandleFunc("GET /{$}", indexHandler)
	http.HandleFunc("GET /", notFoundHandler)

	http.HandleFunc("GET /room/{room}", getRoomHandler)
	http.HandleFunc("GET /room/{room}/update", getRoomUpdateHandler)

	http.HandleFunc("POST /room", createRoomHandler)
	http.HandleFunc("POST /room/{room}", updateRoomHandler)

	log.Println("Server is starting on", listenAddr)
	log.Fatal(http.ListenAndServe(listenAddr, nil))
}

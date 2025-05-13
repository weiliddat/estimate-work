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
	funcs        = template.FuncMap{"join": strings.Join, "slugify": slugify}
	indexTmpl    = template.Must(template.New("index").Funcs(funcs).ParseFS(templatesFs, "templates/base.html", "templates/index.html"))
	roomTmpl     = template.Must(template.New("room").Funcs(funcs).ParseFS(templatesFs, "templates/base.html", "templates/room.html"))
	notFoundTmpl = template.Must(template.New("notFound").Funcs(funcs).ParseFS(templatesFs, "templates/base.html", "templates/not_found.html"))
	rooms        = make(map[string]*Room)
	machineId    string
	persistTime  = 10 * 24 * time.Hour
)

type RenderContext struct {
	User      *User
	Room      *Room
	MachineId string
}

type User struct {
	Id   string
	Name string
}

type Room struct {
	MachineId string
	Id        string
	Name      string

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
		MachineId: machineId,
	}
	rooms[room.Id] = &room
	return &room
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	room, user := getReqRoomUser(r)

	err := indexTmpl.ExecuteTemplate(
		w,
		"base",
		RenderContext{user, room, machineId},
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
	log.Println(machineId, "received request for", r.Method, r.URL)

	urlMachineId := r.PathValue("machine")
	if urlMachineId != machineId {
		w.Header().Add("fly-replay", fmt.Sprintf("instance=%s", urlMachineId))
		log.Println(machineId, "added header to redirect to", urlMachineId)
		notFoundHandler(w, r)
		return
	}

	room, user := getReqRoomUser(r)

	if room == nil {
		notFoundHandler(w, r)
		return
	}

	setMachineId(w, machineId)
	setRoomAndUserId(w, room, user)

	err := roomTmpl.ExecuteTemplate(
		w,
		"base",
		RenderContext{user, room, machineId},
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
		roomUpdates := make(chan bool, 1)

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
		RenderContext{user, room, machineId},
	)
	if err != nil {
		internalErrorHandler(w, r, err)
	}
}

func createRoomHandler(w http.ResponseWriter, r *http.Request) {
	room := NewRoom()
	rooms[room.Id] = room

	http.Redirect(w, r, fmt.Sprintf("/room/%s/%s", machineId, room.Id), http.StatusSeeOther)
}

func updateRoomHandler(w http.ResponseWriter, r *http.Request) {
	log.Println(machineId, "received request for", r.Method, r.URL)

	urlMachineId := r.PathValue("machine")
	if urlMachineId != machineId {
		w.Header().Add("fly-replay", fmt.Sprintf("instance=%s", urlMachineId))
		log.Println(machineId, "added header to redirect to", urlMachineId)
		notFoundHandler(w, r)
		return
	}

	room, user := getReqRoomUser(r)

	if room == nil {
		notFoundHandler(w, r)
		return
	}

	room.mu.Lock()

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

			setMachineId(w, room.MachineId)
			setRoomAndUserId(w, room, user)
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

	showEstimates := r.FormValue("show-estimates")
	if showEstimates == "true" {
		room.Revealed = true
	}

	deleteEstimates := r.FormValue("delete-estimates")
	if deleteEstimates == "true" {
		room.Revealed = false
		room.Estimates = make(map[string]string)
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

	room.mu.Unlock()

	hxRequest := r.Header.Get("hx-request") == "true"
	if hxRequest {
		http.Redirect(w, r, fmt.Sprintf("/room/%s/%s/update", machineId, room.Id), http.StatusSeeOther)
	} else {
		http.Redirect(w, r, fmt.Sprintf("/room/%s/%s", machineId, room.Id), http.StatusSeeOther)
	}
}

func notFoundHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("hx-refresh", "true")
	w.WriteHeader(http.StatusNotFound)
	unsetMachineId(w)
	err := notFoundTmpl.ExecuteTemplate(w, "base", RenderContext{nil, nil, machineId})
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

func readFromDataFile(dataFilePath string) {
	dataFile, err := os.ReadFile(dataFilePath)
	if os.IsNotExist(err) {
		log.Println("Data file does not exist, OK")
	} else if err != nil {
		log.Fatal("Failed to read file: ", err)
	} else {
		dataBytes := bytes.NewBuffer(dataFile)
		dataDecoder := gob.NewDecoder(dataBytes)
		err = dataDecoder.Decode(&rooms)
		if err != nil {
			log.Fatal("Failed to serialize from file: ", err)
		}
		for _, room := range rooms {
			room.MachineId = machineId
		}
		log.Printf("Restored from data file %v rooms", len(rooms))
	}
}

func writeToDataFile(dataFilePath string) {
	for _, r := range rooms {
		r.mu.Lock()
	}
	dataFile, err := os.Create(dataFilePath)
	if err != nil {
		log.Fatal("Failed to create file: ", err)
	}
	dataEncoder := gob.NewEncoder(dataFile)
	err = dataEncoder.Encode(rooms)
	if err != nil {
		log.Fatal("Failed to serialize to file: ", err)
	}
	for _, r := range rooms {
		r.mu.Unlock()
	}
}

func cleanupOldRooms() {
	tenDaysAgo := time.Now().Add(-persistTime)
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

func setRoomAndUserId(w http.ResponseWriter, room *Room, user *User) {
	if room != nil {
		http.SetCookie(w, &http.Cookie{
			Name:   "room",
			Value:  room.Id,
			Path:   "/",
			MaxAge: int(persistTime.Seconds()),
		})
	}

	if user != nil {
		http.SetCookie(w, &http.Cookie{
			Name:   "user",
			Value:  user.Id,
			Path:   "/",
			MaxAge: int(persistTime.Seconds()),
		})
	}
}

func setMachineId(w http.ResponseWriter, machineId string) {
	http.SetCookie(w, &http.Cookie{
		Name:  "machineId",
		Value: machineId,
		Path:  "/",
	})
}

func unsetMachineId(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:  "machineId",
		Value: "",
		Path:  "/",
	})
}

func slugify(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), " ", "-")
}

func main() {
	listenAddr := os.Getenv("LISTEN")
	machineId = os.Getenv("FLY_MACHINE_ID")
	dataFilePath := os.Getenv("DATA_FILE_PATH")

	// Env validation
	if machineId == "" || listenAddr == "" {
		log.Fatal("Missing environment variables")
	}
	if dataFilePath == "" {
		log.Println("No DATA_FILE_PATH provided, rooms are only stored in memory")
	}

	// Restore rooms if a data file is provided
	if dataFilePath != "" {
		gob.Register(Room{})
		readFromDataFile(dataFilePath)
	}

	// Periodic cleanup and dump
	writeInterval := time.NewTicker(1 * time.Second)
	go func() {
		for {
			<-writeInterval.C
			cleanupOldRooms()
			if dataFilePath != "" {
				writeToDataFile(dataFilePath)
			}
		}
	}()

	// HTTP handlers
	http.HandleFunc("GET /{$}", indexHandler)
	http.HandleFunc("GET /", notFoundHandler)

	http.HandleFunc("POST /room", createRoomHandler)
	http.HandleFunc("GET /room/{machine}/{room}", getRoomHandler)
	http.HandleFunc("POST /room/{machine}/{room}", updateRoomHandler)
	http.HandleFunc("GET /room/{machine}/{room}/update", getRoomUpdateHandler)

	log.Println("Server is listening to", listenAddr, "on", machineId)
	log.Fatal(http.ListenAndServe(listenAddr, nil))
}

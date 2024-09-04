package main

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"slices"

	"github.com/google/uuid"
)

//go:embed templates/*.html
var templatesFs embed.FS

var (
	indexTmpl    = template.Must(template.New("index").ParseFS(templatesFs, "templates/base.html", "templates/index.html"))
	roomTmpl     = template.Must(template.New("room").ParseFS(templatesFs, "templates/base.html", "templates/room.html"))
	userTmpl     = template.Must(template.New("user").ParseFS(templatesFs, "templates/base.html", "templates/user.html"))
	notFoundTmpl = template.Must(template.New("user").ParseFS(templatesFs, "templates/base.html", "templates/not_found.html"))
)

type User struct {
	Id   string
	Name string
}

type Room struct {
	Id    string
	Name  string
	Host  User
	Users []User
}

var users = make(map[string]*User)

var rooms = make(map[string]*Room)

func index(w http.ResponseWriter, r *http.Request) {
	user := getUserFromCookies(r)
	prevRoom := getPrevRoomFromCookies(r)

	err := indexTmpl.ExecuteTemplate(
		w,
		"base",
		struct {
			User User
			Room Room
		}{
			User: *user,
			Room: *prevRoom,
		},
	)
	if err != nil {
		log.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "Internal Server Error")
	}
}

func showUser(w http.ResponseWriter, r *http.Request) {
	user := getUserFromCookies(r)
	redirect := r.URL.Query().Get("redirect")

	err := userTmpl.ExecuteTemplate(
		w,
		"base",
		struct {
			User     User
			Redirect string
		}{*user, redirect},
	)
	if err != nil {
		log.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "Internal Server Error")
	}
}

func createUpdateUser(w http.ResponseWriter, r *http.Request) {
	userId := r.FormValue("id")
	userName := r.FormValue("name")
	redirect := r.FormValue("redirect")

	var user *User

	if userId == "" {
		user = newUser(userName)
	} else {
		_, exists := users[userId]
		if exists {
			users[userId].Name = userName
			user = users[userId]
		}
	}

	http.SetCookie(w, &http.Cookie{
		Name:  "user",
		Value: user.Id,
		Path:  "/",
	})

	if redirect != "" {
		http.Redirect(w, r, redirect, http.StatusSeeOther)
	} else {
		http.Redirect(w, r, "/user", http.StatusSeeOther)
	}
}

func joinRoom(w http.ResponseWriter, r *http.Request) {
	roomName := r.PathValue("room")

	room, exists := rooms[roomName]

	if !exists {
		NotFoundHandler(w, r, "room")
		return
	}

	user := getUserFromCookies(r)

	// If user doens't exist we redirect to login with a callback
	if user.Id == "" {
		url := fmt.Sprintf("/user?redirect=/room/%s", room.Id)
		w.Header().Add("hx-location", url)
		http.Redirect(w, r, url, http.StatusSeeOther)
		return
	}

	if room.Host.Id == "" {
		room.Host = *user
	}

	if room.Host.Id != user.Id {
		if !slices.Contains(room.Users, *user) {
			room.Users = append(room.Users, *user)
		}
	}

	http.SetCookie(w, &http.Cookie{
		Name:  "previousRoom",
		Value: room.Id,
		Path:  "/",
	})

	err := roomTmpl.ExecuteTemplate(
		w,
		"base",
		struct {
			User User
			Room Room
		}{
			User: *user,
			Room: *room,
		},
	)
	if err != nil {
		log.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "Internal Server Error")
	}
}

func getRoomUpdates(w http.ResponseWriter, r *http.Request) {
	roomName := r.PathValue("room")

	room, exists := rooms[roomName]

	if !exists {
		NotFoundHandler(w, r, "room")
		return
	}

	user := getUserFromCookies(r)

	err := roomTmpl.ExecuteTemplate(
		w,
		"updates-only",
		struct {
			User User
			Room Room
		}{
			User: *user,
			Room: *room,
		},
	)
	if err != nil {
		log.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "Internal Server Error")
	}
}

func createRoom(w http.ResponseWriter, r *http.Request) {
	room := newRoom()
	rooms[room.Id] = &room

	http.Redirect(w, r, fmt.Sprintf("/room/%s", room.Id), http.StatusSeeOther)
}

func newUser(name string) *User {
	id, _ := uuid.NewV7()

	user := User{
		Id:   id.String(),
		Name: name,
	}

	users[user.Id] = &user

	return &user
}

func getUserFromCookies(r *http.Request) *User {
	userCookie, err := r.Cookie("user")

	var user User

	if err == nil {
		maybeUser, exists := users[userCookie.Value]
		if exists {
			user = *maybeUser
		}
	}

	return &user
}

func newRoom() Room {
	id, _ := uuid.NewV7()
	room := Room{Id: id.String()}
	rooms[room.Id] = &room
	return room
}

func updateRoom(w http.ResponseWriter, r *http.Request) {
	roomName := r.PathValue("room")

	room, exists := rooms[roomName]

	if !exists {
		NotFoundHandler(w, r, "room")
		return
	}

	room.Name = r.FormValue("name")

	http.Redirect(w, r, fmt.Sprintf("/room/%s", room.Id), http.StatusSeeOther)
}

func getPrevRoomFromCookies(r *http.Request) *Room {
	roomCookie, err := r.Cookie("previousRoom")

	var room Room

	if err == nil {
		maybeRoom, exists := rooms[roomCookie.Value]
		if exists {
			room = *maybeRoom
		}
	}

	return &room
}

func NotFoundHandler(w http.ResponseWriter, r *http.Request, entityName string) {
	user := getUserFromCookies(r)
	w.Header().Add("hx-refresh", "true")
	w.WriteHeader(http.StatusNotFound)
	err := notFoundTmpl.ExecuteTemplate(
		w,
		"base",
		struct {
			User   User
			Entity string
		}{*user, entityName},
	)
	if err != nil {
		log.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "Internal Server Error")
	}
}

func main() {
	http.HandleFunc("GET /{$}", index)

	http.HandleFunc("GET /room/{room}", joinRoom)
	http.HandleFunc("GET /room/{room}/update", getRoomUpdates)
	http.HandleFunc("PATCH /room/{room}", updateRoom)
	http.HandleFunc("POST /room", createRoom)

	http.HandleFunc("GET /user", showUser)
	http.HandleFunc("POST /user", createUpdateUser)

	log.Println("Server is starting on port 8080")
	log.Fatal(http.ListenAndServe("localhost:8080", nil))
}

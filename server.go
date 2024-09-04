package main

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"

	"github.com/google/uuid"
)

//go:embed templates/*.html
var templatesFs embed.FS

var (
	indexTmpl = template.Must(template.New("index").ParseFS(templatesFs, "templates/base.html", "templates/index.html"))
	roomTmpl  = template.Must(template.New("room").ParseFS(templatesFs, "templates/base.html", "templates/room.html"))
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

	log.Println(prevRoom)

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
	}
}

func login(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")

	if name == "" {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, "Name is required")
		return
	}

	user := newUser(name)
	users[user.Id] = &user

	http.SetCookie(w, &http.Cookie{
		Name:  "user",
		Value: user.Id,
		Path:  "/",
	})

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func gotoRoom(w http.ResponseWriter, r *http.Request) {
	roomName := r.PathValue("room")

	room, exists := rooms[roomName]

	if !exists {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, "Room not found")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:  "previousRoom",
		Value: room.Id,
		Path:  "/",
	})

	isHtmx := r.Header.Get("hx-request")

	if isHtmx != "" {
		w.Header().Add("hx-push-url", fmt.Sprintf("/room/%s", room.Id))

		err := roomTmpl.ExecuteTemplate(
			w,
			"main",
			struct {
				User User
				Room Room
			}{
				User: *getUserFromCookies(r),
				Room: *room,
			},
		)
		if err != nil {
			log.Println(err)
		}
	} else {
		err := roomTmpl.ExecuteTemplate(
			w,
			"base",
			struct {
				User User
				Room Room
			}{
				User: *getUserFromCookies(r),
				Room: *room,
			},
		)
		if err != nil {
			log.Println(err)
		}
	}
}

func createRoom(w http.ResponseWriter, r *http.Request) {
	room := newRoom()
	rooms[room.Id] = &room

	http.Redirect(w, r, fmt.Sprintf("/room/%s", room.Id), http.StatusSeeOther)
}

func newUser(name string) User {
	id, _ := uuid.NewV7()

	user := User{
		Id:   id.String(),
		Name: name,
	}

	users[user.Id] = &user

	return user
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
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, "Room not found")
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

func main() {
	http.HandleFunc("GET /{$}", index)
	http.HandleFunc("POST /login", login)

	http.HandleFunc("GET /room/{room}", gotoRoom)
	http.HandleFunc("PATCH /room/{room}", updateRoom)
	http.HandleFunc("POST /room", createRoom)

	log.Println("Server is starting on port 8080")
	log.Fatal(http.ListenAndServe("localhost:8080", nil))
}

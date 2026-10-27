package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/ThirdAgeStudio/rs3-plus-one-server/internal/board"
	"github.com/ThirdAgeStudio/rs3-plus-one-server/internal/names"
)

const (
	activeInterval = 4
	idleInterval   = 30
	maxBody        = 8 << 10
)

type handler struct {
	board    *board.Board
	names    NameChecker
	limits   *limits
	serverID string
	log      *slog.Logger
}

type NameChecker interface {
	Exists(ctx context.Context, name, address string) (bool, error)
}

func New(state *board.Board, names NameChecker, limitAddresses bool, serverID string, log *slog.Logger) http.Handler {
	server := &handler{board: state, names: names, limits: newLimits(limitAddresses), serverID: serverID, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /poll", server.poll)
	mux.HandleFunc("POST /listings", server.post)
	mux.HandleFunc("POST /listings/{id}/close", server.close)
	mux.HandleFunc("POST /listings/{id}/apply", server.apply)
	mux.HandleFunc("POST /applications/{id}/accept", server.decide(true))
	mux.HandleFunc("POST /applications/{id}/decline", server.decide(false))
	mux.HandleFunc("POST /applications/{id}/withdraw", server.withdraw)
	mux.HandleFunc("POST /pool/start", server.startPoolParty)
	mux.HandleFunc("POST /pool/parties/{id}/join", server.requestToJoin)
	mux.HandleFunc("POST /pool/requests/{id}/accept", server.answerRequest(true))
	mux.HandleFunc("POST /pool/requests/{id}/decline", server.answerRequest(false))
	mux.HandleFunc("POST /pool/open", server.setOpen)
	mux.HandleFunc("POST /pool/kills", server.updateKills)
	mux.HandleFunc("POST /pool/leave", server.leavePool)
	mux.HandleFunc("POST /listings/restore", server.restoreListing)
	mux.HandleFunc("POST /applications/restore", server.restoreApplication)
	mux.HandleFunc("POST /pool/restore", server.restorePoolParty)
	mux.HandleFunc("POST /pool/rejoin", server.rejoinPool)
	registerDev(server, mux)
	return server.limited(mux)
}

type pollAnswer struct {
	Server   string                `json:"server"`
	Cursor   uint64                `json:"cursor"`
	Interval int                   `json:"interval"`
	Stats    stats                 `json:"stats"`
	Unknown  []string              `json:"unknown,omitempty"`
	Hosting  []board.HostView      `json:"hosting,omitempty"`
	Applied  []board.ApplicantView `json:"applied,omitempty"`
	InPool   []board.PoolView      `json:"inPool,omitempty"`
	Asked    []board.RequestView   `json:"asked,omitempty"`
	Board    *boardChanges         `json:"board,omitempty"`
	Pool     *poolChanges          `json:"pool,omitempty"`
}

type poolChanges struct {
	Full    bool              `json:"full,omitempty"`
	Changed []board.PoolParty `json:"changed"`
	Removed []string          `json:"removed"`
}

type stats struct {
	Listings    int `json:"listings"`
	PoolPlayers int `json:"poolPlayers"`
	Online      int `json:"online"`
}

type boardChanges struct {
	Full    bool            `json:"full,omitempty"`
	Changed []board.Listing `json:"changed"`
	Removed []string        `json:"removed"`
}

func (server *handler) poll(writer http.ResponseWriter, request *http.Request) {
	var body board.PollRequest
	if !server.read(writer, request, &body) {
		return
	}
	result := server.board.Poll(body)
	answer := pollAnswer{
		Server:   server.serverID,
		Cursor:   result.Cursor,
		Interval: idleInterval,
		Stats:    stats{Listings: result.Listings, PoolPlayers: result.PoolPlayers, Online: result.Online},
		Unknown:  result.Unknown,
		Hosting:  result.Hosting,
		Applied:  result.Applied,
		InPool:   result.InPool,
		Asked:    result.Asked,
	}
	if len(body.Tokens) > 0 || body.Board || body.Pool {
		answer.Interval = activeIntervalFor(server.limits.poll())
	}
	if body.Board {
		answer.Board = &boardChanges{Full: result.Full, Changed: result.Changed, Removed: result.Removed}
	}
	if body.Pool {
		answer.Pool = &poolChanges{Full: result.PoolFull, Changed: result.PoolChanged, Removed: result.PoolRemoved}
	}
	server.write(writer, http.StatusOK, answer)
}

type postAnswer struct {
	Listing board.Listing `json:"listing"`
	Token   string        `json:"token"`
}

func (server *handler) post(writer http.ResponseWriter, request *http.Request) {
	var body board.Listing
	if !server.read(writer, request, &body) || !server.realName(writer, request, body.Host) {
		return
	}
	listing, token, err := server.board.Post(body)
	if err != nil {
		server.fail(writer, err)
		return
	}
	server.write(writer, http.StatusOK, postAnswer{Listing: listing, Token: token})
}

type tokenRequest struct {
	Token string `json:"token"`
}

func (server *handler) close(writer http.ResponseWriter, request *http.Request) {
	var body tokenRequest
	if !server.read(writer, request, &body) {
		return
	}
	if err := server.board.Close(request.PathValue("id"), body.Token); err != nil {
		server.fail(writer, err)
		return
	}
	server.write(writer, http.StatusOK, struct{}{})
}

type applyAnswer struct {
	Application board.Application `json:"application"`
	Token       string            `json:"token"`
}

func (server *handler) apply(writer http.ResponseWriter, request *http.Request) {
	var body board.Application
	if !server.read(writer, request, &body) || !server.realName(writer, request, body.Applicant) {
		return
	}
	application, token, err := server.board.Apply(request.PathValue("id"), body)
	if err != nil {
		server.fail(writer, err)
		return
	}
	server.write(writer, http.StatusOK, applyAnswer{Application: application, Token: token})
}

func (server *handler) decide(accept bool) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		var body tokenRequest
		if !server.read(writer, request, &body) {
			return
		}
		application, err := server.board.Decide(request.PathValue("id"), body.Token, accept)
		if err != nil {
			server.fail(writer, err)
			return
		}
		server.write(writer, http.StatusOK, application)
	}
}

func (server *handler) withdraw(writer http.ResponseWriter, request *http.Request) {
	var body tokenRequest
	if !server.read(writer, request, &body) {
		return
	}
	if err := server.board.Withdraw(request.PathValue("id"), body.Token); err != nil {
		server.fail(writer, err)
		return
	}
	server.write(writer, http.StatusOK, struct{}{})
}

type startPoolAnswer struct {
	Party board.PoolParty `json:"party"`
	Token string          `json:"token"`
}

func (server *handler) startPoolParty(writer http.ResponseWriter, request *http.Request) {
	var body board.NewParty
	if !server.read(writer, request, &body) || !server.realName(writer, request, body.Name) {
		return
	}
	party, token, err := server.board.StartPoolParty(body)
	if err != nil {
		server.fail(writer, err)
		return
	}
	server.write(writer, http.StatusOK, startPoolAnswer{Party: party, Token: token})
}

type joinPoolRequest struct {
	Name  string      `json:"name"`
	Kills board.Kills `json:"kills"`
	Boss  int         `json:"boss"`
}

type joinPoolAnswer struct {
	Request board.JoinRequest `json:"request"`
	Token   string            `json:"token"`
}

func (server *handler) requestToJoin(writer http.ResponseWriter, request *http.Request) {
	var body joinPoolRequest
	if !server.read(writer, request, &body) || !server.realName(writer, request, body.Name) {
		return
	}
	asked, token, err := server.board.RequestToJoin(request.PathValue("id"), body.Name, body.Kills, body.Boss)
	if err != nil {
		server.fail(writer, err)
		return
	}
	server.write(writer, http.StatusOK, joinPoolAnswer{Request: asked, Token: token})
}

func (server *handler) answerRequest(accept bool) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		var body tokenRequest
		if !server.read(writer, request, &body) {
			return
		}
		asked, err := server.board.AnswerRequest(request.PathValue("id"), body.Token, accept)
		if err != nil {
			server.fail(writer, err)
			return
		}
		server.write(writer, http.StatusOK, asked)
	}
}

type openRequest struct {
	Token string `json:"token"`
	Open  bool   `json:"open"`
}

func (server *handler) setOpen(writer http.ResponseWriter, request *http.Request) {
	var body openRequest
	if !server.read(writer, request, &body) {
		return
	}
	if err := server.board.SetPoolPartyOpen(body.Token, body.Open); err != nil {
		server.fail(writer, err)
		return
	}
	server.write(writer, http.StatusOK, struct{}{})
}

type killsRequest struct {
	Token string      `json:"token"`
	Kills board.Kills `json:"kills"`
}

func (server *handler) updateKills(writer http.ResponseWriter, request *http.Request) {
	var body killsRequest
	if !server.read(writer, request, &body) {
		return
	}
	if err := server.board.UpdateKills(body.Token, body.Kills); err != nil {
		server.fail(writer, err)
		return
	}
	server.write(writer, http.StatusOK, struct{}{})
}

func (server *handler) leavePool(writer http.ResponseWriter, request *http.Request) {
	var body tokenRequest
	if !server.read(writer, request, &body) {
		return
	}
	if err := server.board.LeavePool(body.Token); err != nil {
		server.fail(writer, err)
		return
	}
	server.write(writer, http.StatusOK, struct{}{})
}

type restoreListingRequest struct {
	Token   string        `json:"token"`
	Listing board.Listing `json:"listing"`
}

func (server *handler) restoreListing(writer http.ResponseWriter, request *http.Request) {
	var body restoreListingRequest
	if !server.read(writer, request, &body) || !server.realName(writer, request, body.Listing.Host) {
		return
	}
	listing, err := server.board.RestoreListing(body.Listing, body.Token)
	if err != nil {
		server.fail(writer, err)
		return
	}
	server.write(writer, http.StatusOK, postAnswer{Listing: listing, Token: body.Token})
}

type restoreApplicationRequest struct {
	board.Application
	Token string `json:"token"`
}

func (server *handler) restoreApplication(writer http.ResponseWriter, request *http.Request) {
	var body restoreApplicationRequest
	if !server.read(writer, request, &body) || !server.realName(writer, request, body.Applicant) {
		return
	}
	application, err := server.board.RestoreApplication(body.Application, body.Token)
	if err != nil {
		server.fail(writer, err)
		return
	}
	server.write(writer, http.StatusOK, applyAnswer{Application: application, Token: body.Token})
}

type restoredParty struct {
	board.NewParty
	ID string `json:"id"`
}

type restorePoolRequest struct {
	Token   string             `json:"token"`
	Party   restoredParty      `json:"party"`
	Members []board.PoolMember `json:"members"`
}

func (server *handler) restorePoolParty(writer http.ResponseWriter, request *http.Request) {
	var body restorePoolRequest
	if !server.read(writer, request, &body) || !server.realName(writer, request, body.Party.Name) {
		return
	}
	party, err := server.board.RestorePoolParty(body.Party.NewParty, body.Party.ID, body.Token, body.Members)
	if err != nil {
		server.fail(writer, err)
		return
	}
	server.write(writer, http.StatusOK, startPoolAnswer{Party: party, Token: body.Token})
}

type rejoinPoolRequest struct {
	joinPoolRequest
	Token   string `json:"token"`
	Party   string `json:"party"`
	Request string `json:"request"`
	Status  string `json:"status"`
}

func (server *handler) rejoinPool(writer http.ResponseWriter, request *http.Request) {
	var body rejoinPoolRequest
	if !server.read(writer, request, &body) || !server.realName(writer, request, body.Name) {
		return
	}
	asked, err := server.board.RejoinPool(body.Party, body.Request, body.Token, body.Name, body.Kills, body.Boss, body.Status)
	if err != nil {
		server.fail(writer, err)
		return
	}
	server.write(writer, http.StatusOK, joinPoolAnswer{Request: asked, Token: body.Token})
}

func (server *handler) read(writer http.ResponseWriter, request *http.Request, into any) bool {
	request.Body = http.MaxBytesReader(writer, request.Body, maxBody)
	if err := json.NewDecoder(request.Body).Decode(into); err != nil {
		server.write(writer, http.StatusBadRequest, errorAnswer{Error: "unreadable request"})
		return false
	}
	return true
}

// a name goes through unchecked if Jagex can't be asked, but waits if lookups are used up, or a flood would get through
func (server *handler) realName(writer http.ResponseWriter, request *http.Request, name string) bool {
	if server.names == nil {
		return true
	}
	cleaned, err := board.CleanName(name)
	if err != nil {
		return true
	}
	exists, err := server.names.Exists(request.Context(), cleaned, address(request))
	var busy names.BusyError
	if errors.As(err, &busy) {
		server.busy(writer, busy.RetryAfter)
		return false
	}
	if err != nil {
		if !errors.Is(err, names.ErrUnavailable) {
			server.log.Warn("couldn't check names", "error", err)
		}
		return true
	}
	if !exists {
		server.write(writer, http.StatusBadRequest, errorAnswer{Error: "No RuneScape player has that name"})
		return false
	}
	return true
}

// the plugin's HTTP can't read headers, so the wait goes in the body too
type errorAnswer struct {
	Error      string `json:"error"`
	RetryAfter int    `json:"retryAfter,omitempty"`
}

func (server *handler) fail(writer http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, board.ErrInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, board.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, board.ErrGone):
		status = http.StatusGone
	case errors.Is(err, board.ErrWrongKey), errors.Is(err, board.ErrNotLeader):
		status = http.StatusForbidden
	case errors.Is(err, board.ErrTokenLive), errors.Is(err, board.ErrIDTaken), errors.Is(err, board.ErrFull),
		errors.Is(err, board.ErrNotPending), errors.Is(err, board.ErrTooMany), errors.Is(err, board.ErrRoleTaken):
		status = http.StatusConflict
	case errors.Is(err, board.ErrBoardFull):
		status = http.StatusServiceUnavailable
	}
	message := err.Error()
	if status == http.StatusInternalServerError {
		server.log.Error("request failed", "error", err)
		message = "server error"
	}
	server.write(writer, status, errorAnswer{Error: message})
}

func (server *handler) write(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		server.log.Warn("couldn't write the answer", "error", err)
	}
}

package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/microcosm-cc/bluemonday"
	"github.com/rs/cors"
	"github.com/tidwall/buntdb"
)

type Comment struct {
	Id      string    `json:"id,omitempty"`
	Domain  string    `json:"domain,omitempty"`
	Path    string    `json:"path,omitempty"`
	Content string    `json:"content,omitempty"`
	Ctime   time.Time `json:"ctime,omitempty"`
	Replies []*Reply  `json:"replies"`
	Issuer
}

type Reply struct {
	Id      string    `json:"id,omitempty"`
	Cid     string    `json:"cid,omitempty"`
	Rid     string    `json:"rid,omitempty"`
	Content string    `json:"content,omitempty"`
	Ctime   time.Time `json:"ctime,omitempty"`
	Issuer
}

type Issuer struct {
	Issuer        string `json:"issuer,omitempty"`
	IssuerWebsite string `json:"issuer_website,omitempty"`
	IssuerEmail   string `json:"issuer_email,omitempty"`
}

type CommentResults struct {
	Done     bool       `json:"done"`
	Comments []*Comment `json:"comments"`
}

// "comment":domain:path:id -> comment
// "reply":cid:id -> reply
var (
	db        *buntdb.DB
	sanitizer = bluemonday.UGCPolicy()
)

const (
	sepChar       = ":"
	prefixComment = "comment"
	prefixReply   = "reply"
	lenId         = 3
	dbfile        = "data/ovo.db"
)

func init() {
	var err error
	os.Mkdir("data", os.ModeDir)

	db, err = buntdb.Open(dbfile)
	if err != nil {
		panic(err)
	}
}

func AddComment(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	var c Comment
	if err = json.Unmarshal(b, &c); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if len(c.Domain) == 0 || len(c.Path) == 0 || len(c.Content) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	c.Id = id6()
	c.Ctime = time.Now()
	c.Content = sanitizer.Sanitize(c.Content)

	if err = addComment(&c); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Write([]byte(`{}`))
}

func AddReply(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	var a Reply
	if err = json.Unmarshal(b, &a); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if len(a.Cid) == 0 || len(a.Content) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	a.Id = id6()
	a.Ctime = time.Now()
	a.Content = sanitizer.Sanitize(a.Content)

	if err = addReply(&a); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Write([]byte(`{}`))
}

func addComment(c *Comment) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}

	key := strings.Join([]string{prefixComment, c.Domain, c.Path, c.Id}, sepChar)
	return db.Update(func(tx *buntdb.Tx) (err error) {
		_, _, err = tx.Set(key, string(b), nil)
		return
	})
}

func addReply(r *Reply) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}

	key := strings.Join([]string{prefixReply, r.Cid, r.Id}, sepChar)
	return db.Update(func(tx *buntdb.Tx) (err error) {
		_, _, err = tx.Set(key, string(b), nil)
		return
	})
}

func ListComments(w http.ResponseWriter, r *http.Request) {
	qs := r.URL.Query()
	domain, path := qs.Get("domain"), qs.Get("path")

	if len(domain) == 0 || len(path) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	cs, err := listComments(domain, path)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	cr := CommentResults{
		Done:     true,
		Comments: cs,
	}

	b, err := json.Marshal(cr)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

func listComments(domain, path string) (cs []*Comment, err error) {
	if len(domain) == 0 {
		domain = "*"
	}
	if len(path) == 0 {
		path = "*"
	}

	key := strings.Join([]string{prefixComment, domain, path, "*"}, sepChar)
	cs = make([]*Comment, 0)

	db.View(func(tx *buntdb.Tx) (err error) {
		err = tx.AscendKeys(key, func(key, value string) bool {
			c := new(Comment)
			if err = json.Unmarshal([]byte(value), c); err != nil {
				return false
			}
			c.IssuerEmail = ""
			cs = append(cs, c)
			return true
		})

		if err != nil {
			log.Printf("listComment.AscendKeys: %v\n", err)
			return
		}

		for _, c := range cs {
			key = strings.Join([]string{prefixReply, c.Id, "*"}, sepChar)
			err = tx.AscendKeys(key, func(key, value string) bool {
				r := new(Reply)

				if err = json.Unmarshal([]byte(value), r); err != nil {
					return false
				}

				r.IssuerEmail = ""
				c.Replies = append(c.Replies, r)

				return true
			})

			if err != nil {
				log.Printf("listComment.AscendKeys: %v\n", err)
				return
			}
		}
		return
	})

	return
}

func id6() string {
	var buf [lenId]byte
	io.ReadFull(rand.Reader, buf[:])
	return fmt.Sprintf("%x", buf)
}

type server struct{}

func (s server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	t := time.Now()
	switch r.Method {
	case http.MethodGet:
		switch r.URL.Path {
		case "/comment":
			ListComments(w, r)
		default:
			http.NotFound(w, r)
		}
	case http.MethodPost:
		switch r.URL.Path {
		case "/comment":
			AddComment(w, r)
		case "/reply":
			AddReply(w, r)
		default:
			http.NotFound(w, r)
		}
	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
	log.Printf("%s %s %d", r.Method, r.URL.Path, time.Since(t))
}

func main() {
	// TODO: use custom cors configuration
	h := cors.Default().Handler(new(server))
	http.ListenAndServe(":9090", h)
}

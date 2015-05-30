package main

import (
	"bytes"
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"github.com/microcosm-cc/bluemonday"
	"github.com/op/go-logging"
	"github.com/russross/blackfriday"
	"golang.org/x/crypto/bcrypt"
	"html/template"
	"io/ioutil"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	DATA_DIR = "data"
)

var (
	listen = ":8080"

	templates = template.Must(template.ParseFiles("templates/base.html"))
	articles  = map[string]bool{}
	users     = map[string]User{}

	log       = logging.MustGetLogger("wiki")
	logFormat = logging.MustStringFormatter("%{color}%{shortfile} %{time:15:04:05} %{level:.4s}%{color:reset} %{message}")
)

type User struct {
	Email    string
	Name     string
	Password []byte
}

func BaseHandler(w http.ResponseWriter, r *http.Request) {
	err := templates.ExecuteTemplate(w, "base.html", nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func HandleArticle(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	switch r.Method {
	case "GET":
		GetArticle(w, r)
	case "PUT":
		UpdateArticle(w, r)
	}
}

func GetArticle(w http.ResponseWriter, r *http.Request) {
	title := r.Form.Get("title")
	format := r.Form.Get("format")

	fileName := fmt.Sprintf("%s/articles/%s.txt", DATA_DIR, title)
	body, err := ioutil.ReadFile(fileName)
	if err != nil {
		log.Info("Could not find requested article: '%s'", title)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	switch format {
	case "markdown":
		fmt.Fprintf(w, string(body))
	case "html":
		processedBody := processMarkdown(body)
		safe := renderMarkdown(processedBody)
		fmt.Fprintf(w, string(safe))
	default:
		log.Info("Invalid format type requested: '%s'", format)
		http.Error(w, err.Error(), 400)
		return
	}
}

func processMarkdown(text []byte) []byte {
	// create wiki links
	rp := regexp.MustCompile(`\[\[([a-zA-z0-9_]+)\]\]`)
	newBody := rp.ReplaceAllStringFunc(string(text), func(str string) (link string) {
		articleName := str[2 : len(str)-2]
		if articles[articleName] {
			link = fmt.Sprintf(`<a href="/%s">%s</a>`, articleName, articleName)
		} else {
			link = fmt.Sprintf(`<a class="wikilink-new" href="/%s">%s</a>`, articleName, articleName)
		}
		return link
	})

	return []byte(newBody)
}

func renderMarkdown(body []byte) []byte {
	htmlFlags := 0 |
		blackfriday.HTML_USE_SMARTYPANTS |
		blackfriday.HTML_SMARTYPANTS_FRACTIONS |
		//TODO: need to add class to generated html
		//blackfriday.HTML_TOC |
		blackfriday.HTML_SMARTYPANTS_LATEX_DASHES

	extensions := 0 |
		blackfriday.EXTENSION_NO_INTRA_EMPHASIS |
		blackfriday.EXTENSION_TABLES |
		blackfriday.EXTENSION_FENCED_CODE |
		blackfriday.EXTENSION_AUTOLINK |
		blackfriday.EXTENSION_STRIKETHROUGH |
		blackfriday.EXTENSION_HEADER_IDS |
		blackfriday.EXTENSION_AUTO_HEADER_IDS |
		blackfriday.EXTENSION_TITLEBLOCK |
		//blackfriday.EXTENSION_SPACE_HEADERS |
		blackfriday.EXTENSION_BACKSLASH_LINE_BREAK

	renderer := blackfriday.HtmlRenderer(htmlFlags, "", "")

	unsafe := blackfriday.MarkdownOptions(body, renderer, blackfriday.Options{
		Extensions: extensions})

	policy := bluemonday.UGCPolicy()
	policy.AllowAttrs("class").OnElements("a")

	safe := policy.SanitizeBytes(unsafe)

	return safe
}

type IncomingArticle struct {
	Title   string
	Body    string
	Summary string
}

func UpdateArticle(w http.ResponseWriter, r *http.Request) {
	decoder := json.NewDecoder(r.Body)
	var article IncomingArticle
	err := decoder.Decode(&article)

	if err != nil {
		log.Info("Couldn't parse article for saving")
		http.Error(w, err.Error(), 400)
		return
	}

	// write article
	fileName := fmt.Sprintf("%s/articles/%s.txt", DATA_DIR, article.Title)
	err = ioutil.WriteFile(fileName, []byte(article.Body), 0644)

	if err != nil {
		log.Error("Error saving article: %s", err)
		http.Error(w, err.Error(), 500)
		return
	}

	articles[article.Title] = true

	writeMetadata(w, r, article)

	archiveArticle(w, article)
}

func archiveArticle(w http.ResponseWriter, article IncomingArticle) {
	var b bytes.Buffer
	gzipWriter := gzip.NewWriter(&b)
	gzipWriter.Write([]byte(article.Body))
	gzipWriter.Close()

	fileName := fmt.Sprintf("%s/archive/%s.%d.txt.gz", DATA_DIR, article.Title, time.Now().Unix())
	err := ioutil.WriteFile(fileName, b.Bytes(), 0644)

	if err != nil {
		log.Error("Error saving archive: %s", err)
		http.Error(w, err.Error(), 500)
		return
	}
}

func writeMetadata(w http.ResponseWriter, r *http.Request, article IncomingArticle) {
	fileName := fmt.Sprintf("%s/metadata/%s.meta", DATA_DIR, article.Title)
	metadataFile, err := os.OpenFile(fileName, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0660)

	if err != nil {
		log.Error("Error saving metadata: %s", err)
		http.Error(w, err.Error(), 500)
		return
	}

	metadata := fmt.Sprintf("%d | %s | %s\n", time.Now().Unix(), r.RemoteAddr, article.Summary)
	fmt.Fprintf(metadataFile, metadata)
}

func HandleRegister(w http.ResponseWriter, r *http.Request) {
	decoder := json.NewDecoder(r.Body)
	var user User
	err := decoder.Decode(&user)

	if err != nil {
		log.Info("Couldn't parse user for registering")
		http.Error(w, err.Error(), 400)
		return
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(user.Password), 10)
	user.Password = hashedPassword

	users[user.Email] = user

	usersFile, err := os.OpenFile(DATA_DIR+"/users.txt", os.O_RDWR|os.O_APPEND|os.O_CREATE, 0660)

	if err != nil {
		log.Error("Couldn't open users file")
	}

	_, err = fmt.Fprintf(usersFile, fmt.Sprintf("%s,%s,%s\n", user.Email, user.Name, user.Password))
	if err != nil {
		log.Error("Couldn't write to users file")
		return
	}

	users[user[0]] = User{user[0], user[1], []byte(user[2])}

	fmt.Fprintf(w, "Good")
}

func HandleLogin(w http.ResponseWriter, r *http.Request) {
	decoder := json.NewDecoder(r.Body)
	var user User
	err := decoder.Decode(&user)

	fmt.Printf("Trying to login with %v", user)

	if err != nil {
		log.Info("Couldn't parse user for login")
		http.Error(w, err.Error(), 400)
		return
	}

	if val, ok := users[user.Email]; ok {
		if bcrypt.CompareHashAndPassword(user.Password, val.Password) == nil {
			// login user
		} else {
			log.Info("Bad password")
			http.Error(w, err.Error(), 400)
		}
	} else {
		log.Info("Couldn't find user")
		http.Error(w, err.Error(), 400)
	}
	fmt.Fprintf(w, "Good")
}

func init() {
	// setup logging
	backend := logging.NewLogBackend(os.Stderr, "", 0)
	backendFormatter := logging.NewBackendFormatter(backend, logFormat)
	logging.SetBackend(backendFormatter)

	articleDir, err := ioutil.ReadDir(DATA_DIR + "/articles")

	if err != nil {
		log.Fatal("Error reading articles: %v", err)
		return
	}

	// populate articles cache
	for _, file := range articleDir {
		if !file.IsDir() {
			articleName := strings.Split(file.Name(), ".")[0]
			articles[articleName] = true
		}
	}

	// populate users
	csvfile, err := os.Open(DATA_DIR + "/users.txt")

	if err != nil {
		log.Fatal("Error reading users")
		return
	}
	defer csvfile.Close()

	reader := csv.NewReader(csvfile)
	reader.FieldsPerRecord = -1

	csvData, err := reader.ReadAll()

	if err != nil {
		log.Fatal("Error reading users")
		return
	}

	for _, user := range csvData {
		users[user[0]] = User{user[0], user[1], []byte(user[2])}
	}
}

func main() {
	http.HandleFunc("/", BaseHandler)
	http.HandleFunc("/article", HandleArticle)

	http.HandleFunc("/user/register", HandleRegister)
	http.HandleFunc("/user/login", HandleLogin)

	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("./static/"))))
	http.Handle("/partials/", http.StripPrefix("/partials/", http.FileServer(http.Dir("./partials/"))))

	log.Notice("Listening on %s", listen)
	http.ListenAndServe(listen, nil)
}

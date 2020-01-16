package main

import (
	"bufio"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/speps/go-hashids"
)

// Default configuration values
const (
	ATAERU_PORT          = "5605"
	ATAERU_STORAGE_DIR   = "./store"
	ATAERU_MAX_FILE_SIZE = "2" // in MB
	ATAERU_PUBLIC_UPLOAD = "true"
)

// config global instance
var APP_CONFIG config

type config struct {
	Port         string
	StorageDir   string
	MaxFileSize  int64 // in mb
	PublicUpload bool
}

func getEnvOrDefault(key string, defaultVal string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}

	return defaultVal
}

func mbToBytes(mb int64) int64 {
	return mb << 20
}

func getBufferFileHash(buf *[]byte) string {
	hash := md5.New()
	return hex.EncodeToString(hash.Sum(*buf)[:16])
}

func isUploadKeyValid(key string) bool {
	// check if key is in keyfile
	keyFile, err := os.Open(filepath.Join(APP_CONFIG.StorageDir, "/keys"))
	if err != nil {
		log.Printf("Error while opening keyfile: %s", err.Error())
		return false
	}

	validKey := false
	scanner := bufio.NewScanner(keyFile)
	for scanner.Scan() {
		if key == scanner.Text() {
			validKey = true
		}
	}
	if err := scanner.Err(); err != nil {
		log.Printf("Error while reading from scanner: %s", err)
	}

	return validKey
}

// initializeEnv initializes program configuration from enviroment variables and ensure directory structure is created
func initializeEnv() (config, error) {
	var conf config
	var err error
	// port server listens on
	port := getEnvOrDefault("ATAERU_PORT", ATAERU_PORT)
	storageDir := getEnvOrDefault("ATAERU_STORAGE_DIR", ATAERU_STORAGE_DIR)
	maxFileSize := getEnvOrDefault("ATAERU_MAX_FILE_SIZE", ATAERU_MAX_FILE_SIZE)
	publicUpload := getEnvOrDefault("ATAERU_PUBLIC_UPLOAD", ATAERU_PUBLIC_UPLOAD)

	// if the storageDir path doesn't exist, create it!
	if _, err := os.Stat(storageDir); os.IsNotExist(err) {
		if err = os.MkdirAll(storageDir, os.ModePerm); err != nil {
			return conf, fmt.Errorf("Error while trying to create storage directory: %s", err.Error())
		}

		if err = os.MkdirAll(filepath.Join(storageDir, "/files"), os.ModePerm); err != nil {
			return conf, fmt.Errorf("Error while trying to create files directory: %s", err.Error())
		}

		if err = os.MkdirAll(filepath.Join(storageDir, "/hashes"), os.ModePerm); err != nil {
			return conf, fmt.Errorf("Error while trying to create hashes directory: %s", err.Error())
		}

		if _, err := os.Create(filepath.Join(storageDir, "/keys")); err != nil {
			return conf, fmt.Errorf("Error while trying to create keys file: %s", err.Error())
		}
	}

	// convert non string values
	maxFileSizeConv, err := strconv.ParseInt(maxFileSize, 10, 64)
	publicUploadConv, err := strconv.ParseBool(publicUpload)
	if err != nil {
		return conf, fmt.Errorf("Error while converting strings to native values: %s", err.Error())
	}

	// init our conf struct
	conf = config{Port: port, StorageDir: storageDir, MaxFileSize: maxFileSizeConv, PublicUpload: publicUploadConv}

	return conf, nil
}

/* Handlers */

func disableDirListing(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// indexHandler passes to either landingPage or uploadHandler, depending on request type and content
func indexHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	// pass to landing on get
	case http.MethodGet:
		landingPage(w, r)
		break
	// upload landing on post
	case http.MethodPost:
		uploadHandler(w, r)
		break
	default:
		// create error message for unsupported method
		w.Write([]byte(fmt.Sprintf("HTTP Method type %s is unsupported on /\n", r.Method)))
		break
	}

}

func landingPage(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("<h1>Ataeru</h1>"))
}

func uploadHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("File upload initiated!")
	// convert our MB size limit to bytes (by shifting 20 bits), and compare with the file size
	byteMaxSize := mbToBytes(APP_CONFIG.MaxFileSize)
	// specified maximmum upload size
	r.ParseMultipartForm(byteMaxSize)

	// if public uploading is disabled, make sure the user has a valid key
	if APP_CONFIG.PublicUpload == false {
		var key string
		if key = r.FormValue("key"); key == "" {
			w.Write([]byte("Public uploading is currently disabled, go away\n"))
			return
		}

		if isUploadKeyValid(key) != true {
			w.Write([]byte("Incorrect key, sorry gotta go!\n"))
			return
		}
	}

	file, handler, err := r.FormFile("file")
	defer file.Close()
	if err != nil {
		log.Printf("Error retriving file from multipart form: %s", err.Error())
		return
	}
	// disable when done debugging (probably)
	log.Printf("Uploaded File: %+v\n", handler.Filename)
	log.Printf("File Size: %+v\n", handler.Size)
	log.Printf("MIME Header: %+v\n", handler.Header)

	// file's too big
	if handler.Size > byteMaxSize {
		w.Write([]byte(fmt.Sprintf("The maximum file size is currently %dMB, you uploaded a %dMB file...\n", APP_CONFIG.MaxFileSize, handler.Size>>20)))
		return
	}

	// generate timestamp, then create a hashid from it
	stamp := time.Now().Unix() << 32
	hashData := hashids.NewData()
	hashData.MinLength = 6
	generator, err := hashids.NewWithData(hashData)

	id, err := generator.Encode([]int{int(stamp)})

	if err != nil {
		log.Printf("error while creating hashid: %s", err)
		return
	}

	// read contents of form file into buffer
	buf, err := ioutil.ReadAll(file)
	if err != nil {
		log.Printf("Error while reading multipart file into buffer: %s", err.Error())
		return
	}

	// filePath is the storage dir/files, and our id+the uploaded file's extension
	uploadedFilename := id + filepath.Ext(handler.Filename)
	filePath := filepath.Join(filepath.Join(APP_CONFIG.StorageDir, "/files"), uploadedFilename)

	// get hash of file to check if it's already been uploaded
	hash := getBufferFileHash(&buf)
	hashPath := filepath.Join(APP_CONFIG.StorageDir, "/hashes", hash)
	// check if filename with hash exists under /hashes
	if _, err := os.Stat(hashPath); err == nil {
		// hash exists, give user the already existant file
		hashFilename, err := ioutil.ReadFile(hashPath)
		if err != nil {
			log.Printf("Error while trying to read hashfile!: %s", err)
		}

		// new filename is the contents of the read hashfile
		filePath = filepath.Join(filepath.Join(APP_CONFIG.StorageDir, "/files"), string(hashFilename))
	} else {
		// create file with the filename as it's contents, give it the name of the hash
		err = ioutil.WriteFile(hashPath, []byte(uploadedFilename+"\n"), 0644)
		if err != nil {
			log.Printf("Error while attempting to write hashfile: %s", err.Error())
		}

		// write buffer to to new file
		err = ioutil.WriteFile(filePath, buf, 0644)
		if err != nil {
			log.Printf("Eror while attempting to write buffer to new file: %s", err.Error())
			return
		}
	}

	// send the user back the location of the file
	w.Write([]byte(fmt.Sprintf("http://localhost:%s/storage/%s\n", APP_CONFIG.Port, filepath.Base(filePath))))
}

func main() {
	// get our configuration nice and orderly
	conf, err := initializeEnv()

	// log & exit on env init error
	if err != nil {
		log.Fatalln(err)
	}

	// global config so handlers can access (yeah yeah globals but this is a small program who cares)
	APP_CONFIG = conf

	// create router
	mux := http.NewServeMux()

	// fileserver for uploaded files
	fs := http.FileServer(http.Dir(filepath.Join(conf.StorageDir, "/files")))
	mux.Handle("/storage/", http.StripPrefix("/storage/", disableDirListing(fs)))

	// index (routes between landing and upload)
	mux.HandleFunc("/", indexHandler)

	log.Printf("Attempting to listen on :%s", conf.Port)
	// start that bitch up
	if err := http.ListenAndServe(":"+conf.Port, mux); err != nil {
		log.Fatalf("Error while starting http server!: %s", err.Error())
	}
}

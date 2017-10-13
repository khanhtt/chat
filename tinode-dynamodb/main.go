package main

import (
	"encoding/json"
	"flag"
	"io/ioutil"
	"log"

	_ "github.com/tinode/chat/server/db/dynamodb"
	"github.com/tinode/chat/server/store"
)

type configType struct {
	StoreConfig json.RawMessage `json:"store_config"`
}

func main() {
	configFile := flag.String("config", "./config.json", "database configuration")
	reset := flag.Bool("reset", false, "delete existing tables?")
	flag.Parse()

	var config configType
	if b, err := ioutil.ReadFile(*configFile); err != nil {
		log.Panic(err)
	} else if err = json.Unmarshal(b, &config); err != nil {
		log.Panic(err)
	}

	// create the whole thing
	err := store.Open(string(config.StoreConfig))
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	err = store.InitDb(*reset)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("Successfully initialize dynamodb database")
}

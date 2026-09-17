package main

import (
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

func main() {
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fmt.Printf("This is Request.No.%d\n", i)
			response, err := http.Get("http://localhost:8080/readyz")
			if err != nil {
				log.Fatal(err)
			}
			fmt.Println(response.Status)
		}()
		time.Sleep(1 * time.Second)
	}
	wg.Wait()
	fmt.Println("10 of requests all done.")

}

package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
)

type Result struct {
	Total   int
	Skipped int
	Counts  map[string]int
}

type statCount struct {
	code  string
	count int
}

func main() {
	topptr := flag.Int("top", 10, "how many should be display")
	flag.Parse()
	filepathes := flag.Args()

	// check if files arguments wetiher nil not
	// length of filepathes either >= or == 0, never < 0
	if len(filepathes) == 0 {
		flag.Usage()
		log.Fatal("File path accquire")
	}

	ch := make(chan Result)
	var wg sync.WaitGroup

	for _, filepath := range filepathes {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			// open file
			file, err := os.Open(p)
			if err != nil {
				log.Printf("open file error: %s, pass %s", err, p)
				return
			}
			defer file.Close()

			// call CountStatus
			result, err := CountStatus(file)
			if err != nil {
				log.Printf("Scanfile error: %s, pass %s", err, p)
				return
			}

			// send Result to ch
			ch <- result
		}(filepath)
	}

	// indivadual goroutine escord whenn all worker done
	go func() {
		wg.Wait()
		close(ch)
	}()

	received := 0
	// main continue take all result and merge
	total := Result{Counts: map[string]int{}}
	for r := range ch {
		received++
		// merge r into total
		total.Total += r.Total
		total.Skipped += r.Skipped
		// result1.Counts {"200":3, "404":1, "500":1}
		// result2.Counts {"200":1, "404":1, "500":1}
		// result3.Counts {"200":1, "404":1, "500":0}
		for rhttpcode, roccurrence := range r.Counts {
			total.Counts[rhttpcode] += roccurrence
		}
	}
	failed := received < len(filepathes)

	fmt.Println("Total lines: ", total.Total, ", Skipped: ", total.Skipped)

	var statCountSlice []statCount
	for statuscode, times := range total.Counts {
		statCountSlice = append(statCountSlice, statCount{statuscode, times})
	}

	// tie deal
	// statCountSlice be like: [{"500", 1}, {"404", 1}, {"200", 3}], which order is a mess
	sort.Slice(statCountSlice, func(i, j int) bool {
		a, b := statCountSlice[i], statCountSlice[j]
		if a.count != b.count {
			return a.count > b.count
		}
		return a.code < b.code
	})
	// top must >= 0
	if *topptr < 0 {
		log.Fatal("--top must be >= 0")
	}
	// for situation as top gt linecount(actually, top need less than len(contermap)
	// considerate lapse, total:5 lapse:2 skipped:0 len(countermap):3)
	if *topptr >= len(total.Counts) {
		*topptr = len(total.Counts)
	}

	fmt.Println("By status:")

	for _, sc := range statCountSlice[:*topptr] {
		fmt.Printf("%5s  %d\n", sc.code, sc.count)
	}

	if failed {
		os.Exit(1)
	}

}

func CountStatus(r io.Reader) (Result, error) {
	scanner := bufio.NewScanner(r)
	var linecount, skippedcount int
	countmap := make(map[string]int)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		linecount++
		if len(fields) != 3 {
			skippedcount++
			continue
		}
		// lacks format like "StatusCode Method Path" validate
		countmap[fields[0]]++
	}
	if err := scanner.Err(); err != nil {
		return Result{}, err
	}

	return Result{
		Total:   linecount,
		Skipped: skippedcount,
		Counts:  countmap,
	}, nil
}

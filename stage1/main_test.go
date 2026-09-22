package main

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

func TestCountStatus(t *testing.T) {
	tests := []struct {
		name    string
		input   io.Reader
		want    Result
		wantErr bool
	}{
		{
			"ReadNormalMutiLines",
			strings.NewReader("200 GET /\n404 GET /alien"),
			Result{
				Total:   2,
				Skipped: 0,
				Counts:  map[string]int{"200": 1, "404": 1},
			},
			false,
		},
		{
			"ReadEmpty",
			strings.NewReader(""),
			Result{
				Total:   0,
				Skipped: 0,
				Counts:  map[string]int{},
			},
			false,
		},
		{
			"ReadAbnormalMutiLines",
			strings.NewReader("200 GET /\nGET 200 /\n404 STRENGE"),
			Result{
				Total:   3,
				Skipped: 1,
				// for lacks "HTTPCode Method Path" format validate, just validate
				// length of input whether is nor 3.
				Counts: map[string]int{"200": 1, "GET": 1},
			},
			false,
		},
		{
			"ReadError",
			iotest.ErrReader(errors.New("ICan'tBeRead")),
			Result{},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CountStatus(tt.input)
			// case1: i want Err, but got nil
			// case2: i don't wanna Err, but get err
			if (err != nil) != tt.wantErr {
				t.Errorf("wantErr= %v, got err= %v", tt.wantErr, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v\nwant %v", got, tt.want)
			}
		})
	}
}

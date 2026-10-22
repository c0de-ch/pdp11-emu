package simh

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

// frame feeds input to a framer in the given chunk sizes and returns the
// events: "L:<line>" and "P".
func frame(input string, chunks []int) []string {
	var ev []string
	f := framer{
		onLine:   func(l string) { ev = append(ev, "L:"+l) },
		onPrompt: func() { ev = append(ev, "P") },
	}
	b := []byte(input)
	for _, n := range chunks {
		if n > len(b) {
			n = len(b)
		}
		f.feed(b[:n])
		b = b[n:]
	}
	f.feed(b)
	f.flush()
	return ev
}

func TestFramer(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name: "start-up",
			input: "\nPDP-11 simulator Open SIMH V4.1-0 Current        git commit id: 87eb7d5e\nDisabling XQ\n" +
				"/data/run/run.ini-6> set console -u telnet=127.0.0.1:47100;notelnet\n%SIM-INFO: Listening on port 127.0.0.1:47100\nsim> ",
			want: []string{"L:", "L:PDP-11 simulator Open SIMH V4.1-0 Current        git commit id: 87eb7d5e", "L:Disabling XQ",
				"L:/data/run/run.ini-6> set console -u telnet=127.0.0.1:47100;notelnet",
				"L:%SIM-INFO: Listening on port 127.0.0.1:47100", "P"},
		},
		{
			name:  "stop after run: CRLF info line, blank line, stop line",
			input: "%SIM-INFO: Running with Buffered Console\r\n\nHALT instruction, PC: 001006 (HALT)\nsim> ",
			want:  []string{"L:%SIM-INFO: Running with Buffered Console", "L:", "L:HALT instruction, PC: 001006 (HALT)", "P"},
		},
		{
			name:  "examine replies",
			input: "R00:\t000000\nsim> PC:\t000104\nsim> ",
			want:  []string{"L:R00:\t000000", "P", "L:PC:\t000104", "P"},
		},
		{
			name:  "empty reply",
			input: "sim> sim> sim> ",
			want:  []string{"P", "P", "P"},
		},
		{
			name:  "output after a prompt",
			input: "\nSIGTERM received, PC: 002000 (BR 2000)\nsim> Goodbye\n",
			want:  []string{"L:", "L:SIGTERM received, PC: 002000 (BR 2000)", "P", "L:Goodbye"},
		},
		{
			name:  "reply starting with a blank line",
			input: "sim> \nHALT instruction, PC: 001002 (HALT)\nsim> ",
			want:  []string{"P", "L:", "L:HALT instruction, PC: 001002 (HALT)", "P"},
		},
		{
			name:  "prompt-like text inside a line",
			input: "echo: sim> not a prompt\n",
			want:  []string{"L:echo: sim> not a prompt"},
		},
		{
			name:  "partial prompt at EOF is a line",
			input: "sim>",
			want:  []string{"L:sim>"},
		},
		{
			name:  "unterminated output at EOF",
			input: "Goodbye",
			want:  []string{"L:Goodbye"},
		},
		{
			name:  "several CRs",
			input: "a\r\r\nb\r\n",
			want:  []string{"L:a", "L:b"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// One chunk, byte by byte, and random chunkings must all agree:
			// pipe reads split the stream anywhere.
			if got := frame(tc.input, nil); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("one chunk:\n got %q\nwant %q", got, tc.want)
			}
			ones := make([]int, len(tc.input))
			for i := range ones {
				ones[i] = 1
			}
			if got := frame(tc.input, ones); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("byte by byte:\n got %q\nwant %q", got, tc.want)
			}
			rng := rand.New(rand.NewSource(1))
			for i := 0; i < 50; i++ {
				var chunks []int
				for n := 0; n < len(tc.input); {
					c := 1 + rng.Intn(7)
					chunks = append(chunks, c)
					n += c
				}
				if got := frame(tc.input, chunks); !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("chunks %v:\n got %q\nwant %q", chunks, got, tc.want)
				}
			}
		})
	}
}

func TestFramerLongLine(t *testing.T) {
	long := strings.Repeat("x", maxLine+10)
	got := frame(long+"\nsim> ", []int{maxLine / 2, maxLine / 2, 5})
	if len(got) != 3 || got[2] != "P" || len(got[0])+len(got[1]) != len(long)+4 {
		t.Fatalf("got %d events: %.40q", len(got), got)
	}
}

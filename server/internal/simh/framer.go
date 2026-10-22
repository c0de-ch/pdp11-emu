package simh

import "bytes"

// prompt is SimH's command prompt. It is printed without a newline and
// flushed; SimH then blocks reading stdin, so the CPU is halted exactly when
// the stdout stream ends with these bytes.
const prompt = "sim> "

// maxLine bounds a line without a newline (runaway output); longer
// fragments are delivered as separate lines.
const maxLine = 64 << 10

// framer splits the simulator's stdout byte stream into lines and prompts.
//
// Lines end in "\n" or "\r\n" (trailing CRs are removed: SimH writes CRLF
// while the CPU runs). A prompt is recognised at the start of a line: either
// the unterminated tail is exactly "sim> ", or a line starts with it (output
// that followed a prompt, e.g. "sim> Goodbye"). Callbacks run synchronously
// on the goroutine that calls feed.
type framer struct {
	partial  []byte
	onLine   func(line string)
	onPrompt func()
}

func (f *framer) feed(p []byte) {
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			f.partial = append(f.partial, p...)
			break
		}
		f.partial = append(f.partial, p[:i]...)
		p = p[i+1:]
		f.emit(true)
	}
	f.prompts()
	if len(f.partial) >= maxLine {
		f.emit(false)
	}
}

// flush delivers an unterminated tail (at EOF).
func (f *framer) flush() {
	if f.prompts(); len(f.partial) > 0 {
		f.emit(false)
	}
}

// prompts consumes the prompts at the start of the pending bytes.
func (f *framer) prompts() {
	for bytes.HasPrefix(f.partial, []byte(prompt)) {
		f.partial = append(f.partial[:0], f.partial[len(prompt):]...)
		f.onPrompt()
	}
}

// emit delivers the pending bytes, after any leading prompts, as one line.
// An empty line after a prompt is real output: a run command's reply starts
// with the blank line before the stop line.
func (f *framer) emit(terminated bool) {
	f.prompts()
	b := f.partial
	if terminated {
		b = bytes.TrimRight(b, "\r")
	}
	f.onLine(string(b))
	f.partial = f.partial[:0]
}

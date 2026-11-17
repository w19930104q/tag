// Copyright 2015, David Howden
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// synchsafe encodes n as a 7-bit-per-byte synchsafe integer (ID3v2.4 sizes).
func synchsafe(n uint) []byte {
	return []byte{
		byte(n >> 21),
		byte(n >> 14),
		byte(n >> 7),
		byte(n),
	}
}

// be24 / be32 encode plain big-endian sizes (ID3v2.2 / 2.3).
func be24(n uint) []byte {
	return []byte{byte(n >> 16), byte(n >> 8), byte(n)}
}

func be32(n uint) []byte {
	return []byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
}

// id3v2HeaderBytes builds a 10-byte ID3v2 tag header.
func id3v2HeaderBytes(version byte, flags byte, bodySize uint) []byte {
	h := []byte{'I', 'D', '3', version, 0, flags}
	h = append(h, synchsafe(bodySize)...)
	return h
}

// id3v23ExtHeader builds an ID3v2.3 extended header (size prefix excludes
// itself) followed by payload bytes.
func id3v23ExtHeader(payload []byte) []byte {
	return append(be32(uint(len(payload))), payload...)
}

// id3v24ExtHeader builds an ID3v2.4 extended header (synchsafe size includes
// the 4 size bytes themselves).
func id3v24ExtHeader(payload []byte) []byte {
	return append(synchsafe(uint(len(payload))+4), payload...)
}

// frameV22 encodes an ID3v2.2 frame: 3-byte id, 3-byte size, payload (there
// are no per-frame flags in 2.2).
func frameV22(name string, payload []byte) []byte {
	b := []byte(name)
	b = append(b, be24(uint(len(payload)))...)
	return append(b, payload...)
}

// frameV23 encodes an ID3v2.3 frame: 4-byte id, plain 32-bit size, 2 flag
// bytes, payload.
func frameV23(name string, flags uint16, payload []byte) []byte {
	b := []byte(name)
	b = append(b, be32(uint(len(payload)))...)
	b = append(b, byte(flags>>8), byte(flags))
	return append(b, payload...)
}

// frameV24 encodes an ID3v2.4 frame: 4-byte id, synchsafe size, 2 flag bytes,
// payload.
func frameV24(name string, flags uint16, payload []byte) []byte {
	b := []byte(name)
	b = append(b, synchsafe(uint(len(payload)))...)
	b = append(b, byte(flags>>8), byte(flags))
	return append(b, payload...)
}

// textPayload builds an ISO-8859-1 text frame payload.
func textPayload(s string) []byte {
	return append([]byte{0}, []byte(s)...)
}

func assertErrClass(t *testing.T, err error, class ID3v2ErrorClass) *ID3v2Error {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error of class %q, got nil", class)
	}
	var e *ID3v2Error
	if !errors.As(err, &e) {
		t.Fatalf("expected *ID3v2Error, got %T: %v", err, err)
	}
	if e.Class != class {
		t.Fatalf("expected error class %q, got %q (full error: %v)", class, e.Class, err)
	}
	return e
}

// runStateMachine feeds body directly to the frame state machine and returns
// its observable end state (or the classified error).
func runStateMachine(t *testing.T, v Format, body []byte, extHeader bool) (*id3v2ParseResult, error) {
	t.Helper()
	h := &id3v2Header{Version: v, ExtendedHeader: extHeader, Size: uint(len(body))}
	base := uint(0)
	if extHeader {
		n, err := skipID3v2ExtendedHeader(body, h)
		if err != nil {
			return nil, id3v2Err(ID3v2ErrorExtendedHeader, 0, "", "%v", err)
		}
		base = n
	}
	r := io.LimitReader(bytes.NewReader(body[base:]), int64(len(body)-int(base)))
	return readID3v2Frames(r, uint(len(body))-base, base, h, body)
}

func TestReadID3v2FramesHappyPath(t *testing.T) {
	tests := []struct {
		name    string
		version Format
		body    []byte
		want    []ID3v2FrameRecord
	}{
		{
			name:    "v2.2 two text frames",
			version: ID3v2_2,
			body: append(
				frameV22("TT2", textPayload("Song")),
				frameV22("TP1", textPayload("Band"))...),
			want: []ID3v2FrameRecord{
				{Name: "TT2", Size: 5, Consumed: 6 + 5},
				{Name: "TP1", Size: 5, Consumed: 6 + 5},
			},
		},
		{
			name:    "v2.3 two text frames",
			version: ID3v2_3,
			body: append(
				frameV23("TIT2", 0, textPayload("Song")),
				frameV23("TPE1", 0, textPayload("Band"))...),
			want: []ID3v2FrameRecord{
				{Name: "TIT2", Size: 5, Consumed: 10 + 5},
				{Name: "TPE1", Size: 5, Consumed: 10 + 5},
			},
		},
		{
			name:    "v2.4 two text frames",
			version: ID3v2_4,
			body: append(
				frameV24("TIT2", 0, textPayload("Song")),
				frameV24("TPE1", 0, textPayload("Band"))...),
			want: []ID3v2FrameRecord{
				{Name: "TIT2", Size: 5, Consumed: 10 + 5},
				{Name: "TPE1", Size: 5, Consumed: 10 + 5},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := runStateMachine(t, tt.version, tt.body, false)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Reason != stopEndOfTag {
				t.Errorf("stop reason = %v, expected stopEndOfTag", res.Reason)
			}
			if res.Offset != uint(len(tt.body)) {
				t.Errorf("consumed offset = %d, expected full body %d", res.Offset, len(tt.body))
			}
			if len(res.Records) != len(tt.want) {
				t.Fatalf("records = %v, want %v", res.Records, tt.want)
			}
			for i, want := range tt.want {
				if res.Records[i] != want {
					t.Errorf("record[%d] = %+v, want %+v", i, res.Records[i], want)
				}
			}
		})
	}
}

func TestReadID3v2FramesPadding(t *testing.T) {
	t.Run("v2.3 zero padding after frame stops cleanly", func(t *testing.T) {
		frame := frameV23("TIT2", 0, textPayload("Song"))
		body := append([]byte{}, frame...)
		body = append(body, make([]byte, 32)...) // explicit padding zone

		res, err := runStateMachine(t, ID3v2_3, body, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Reason != stopPadding {
			t.Errorf("stop reason = %v, want stopPadding", res.Reason)
		}
		if len(res.Records) != 1 || res.Records[0].Name != "TIT2" {
			t.Errorf("records = %v, want single TIT2", res.Records)
		}
	})

	t.Run("v2.4 truncated non-zero tail is frame-header error", func(t *testing.T) {
		frame := frameV24("TIT2", 0, textPayload("Song"))
		body := append([]byte{}, frame...)
		body = append(body, 0x54, 0x49, 0x54) // 3 garbage bytes, no room for a 10-byte header

		_, err := runStateMachine(t, ID3v2_4, body, false)
		e := assertErrClass(t, err, ID3v2ErrorFrameHeader)
		if e.Offset != uint(len(frame)) {
			t.Errorf("error offset = %d, want %d", e.Offset, len(frame))
		}
	})

	t.Run("v2.2 all-zero body is padding, not a loop", func(t *testing.T) {
		body := make([]byte, 20)
		res, err := runStateMachine(t, ID3v2_2, body, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Reason != stopPadding || len(res.Records) != 0 {
			t.Errorf("got reason=%v records=%v, want padding with no records", res.Reason, res.Records)
		}
	})
}

func TestReadID3v2FramesBadFrameSize(t *testing.T) {
	t.Run("v2.3 known frame with size beyond tag body", func(t *testing.T) {
		// TIT2 header claiming 4096 bytes while the tag only has 4 bytes left.
		body := []byte("TIT2")
		body = append(body, be32(4096)...)
		body = append(body, 0, 0) // frame flags
		body = append(body, 'x', 'x', 'x', 'x')

		_, err := runStateMachine(t, ID3v2_3, body, false)
		e := assertErrClass(t, err, ID3v2ErrorFrameSize)
		if e.Frame != "TIT2" {
			t.Errorf("error frame = %q, want TIT2", e.Frame)
		}
		if e.Offset != 0 {
			t.Errorf("error offset = %d, want 0 (cursor stays at frame start)", e.Offset)
		}
	})

	t.Run("v2.4 synchsafe frame size beyond tag body", func(t *testing.T) {
		body := []byte("TPE1")
		body = append(body, synchsafe(1000)...)
		body = append(body, 0, 0)
		body = append(body, 1, 2)

		_, err := runStateMachine(t, ID3v2_4, body, false)
		e := assertErrClass(t, err, ID3v2ErrorFrameSize)
		if e.Frame != "TPE1" || e.Offset != 0 {
			t.Errorf("got frame=%q offset=%d, want TPE1 at 0", e.Frame, e.Offset)
		}
	})

	t.Run("v2.2 unknown frame overflowing is tolerated as padding", func(t *testing.T) {
		// "XYZ" is not a defined v2.2 frame; an overflow there must not abort
		// parsing of frames already seen.
		good := frameV22("TT2", textPayload("Song"))
		bad := []byte{'X', 'Y', 'Z'}
		bad = append(bad, be24(500)...) // 3-byte 24-bit size
		body := append(good, bad...)

		res, err := runStateMachine(t, ID3v2_2, body, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Reason != stopPadding {
			t.Errorf("reason = %v, want stopPadding", res.Reason)
		}
		if len(res.Records) != 1 || res.Records[0].Name != "TT2" {
			t.Errorf("records = %v, want single TT2", res.Records)
		}
	})

	t.Run("v2.3 zero-size frame with non-zero tail is an error", func(t *testing.T) {
		body := []byte("TIT2")
		body = append(body, be32(0)...)
		body = append(body, 0, 0)
		body = append(body, 0x01, 0x02, 0x03) // non-zero garbage where padding should be

		_, err := runStateMachine(t, ID3v2_3, body, false)
		assertErrClass(t, err, ID3v2ErrorFrameHeader)
	})
}

func TestReadID3v2ExtendedHeader(t *testing.T) {
	t.Run("v2.3 extended header then frames", func(t *testing.T) {
		ext := id3v23ExtHeader([]byte{0x00, 0x00, 0x00, 0x06})
		frame := frameV23("TIT2", 0, textPayload("Song"))
		body := append(ext, frame...)

		res, err := runStateMachine(t, ID3v2_3, body, true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(res.Records) != 1 || res.Records[0].Name != "TIT2" {
			t.Fatalf("records = %v, want TIT2 after extended header", res.Records)
		}
		if res.Offset != uint(len(body)) {
			t.Errorf("offset = %d, want %d", res.Offset, len(body))
		}
	})

	t.Run("v2.4 extended header then frames", func(t *testing.T) {
		ext := id3v24ExtHeader(make([]byte, 6))
		frame := frameV24("TIT2", 0, textPayload("Song"))
		body := append(ext, frame...)

		res, err := runStateMachine(t, ID3v2_4, body, true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(res.Records) != 1 || res.Records[0].Name != "TIT2" {
			t.Fatalf("records = %v, want TIT2", res.Records)
		}
	})

	t.Run("v2.3 extended header length beyond body", func(t *testing.T) {
		body := append(be32(9999), 0, 0, 0) // claims 9999 bytes in a 7-byte body
		h := &id3v2Header{Version: ID3v2_3, ExtendedHeader: true, Size: uint(len(body))}
		_, err := skipID3v2ExtendedHeader(body, h)
		if err == nil {
			t.Fatal("expected extended header bounds error")
		}
	})

	t.Run("v2.4 extended header synchsafe underflow", func(t *testing.T) {
		body := append(synchsafe(2), 0, 0) // 2 < 4, would underflow the old `- 4` math
		h := &id3v2Header{Version: ID3v2_4, ExtendedHeader: true, Size: uint(len(body))}
		_, err := skipID3v2ExtendedHeader(body, h)
		if err == nil {
			t.Fatal("expected extended header range error")
		}
	})

	t.Run("v2.4 extended header larger than body", func(t *testing.T) {
		body := append(synchsafe(500), make([]byte, 10)...)
		h := &id3v2Header{Version: ID3v2_4, ExtendedHeader: true, Size: uint(len(body))}
		_, err := skipID3v2ExtendedHeader(body, h)
		if err == nil {
			t.Fatal("expected extended header range error")
		}
	})
}

// TestReadID3v2TagsEndToEnd drives the public ReadID3v2Tags entry point with
// fully synthetic tag streams.
func TestReadID3v2TagsEndToEnd(t *testing.T) {
	t.Run("v2.3 corrupted frame size never reads audio body", func(t *testing.T) {
		good := frameV23("TIT2", 0, textPayload("Song"))
		bad := []byte("TPE1")
		bad = append(bad, be32(10000)...) // claims 10k bytes
		bad = append(bad, 0, 0)
		tagBody := append(good, bad...)

		var stream bytes.Buffer
		stream.Write(id3v2HeaderBytes(3, 0, uint(len(tagBody))))
		stream.Write(tagBody)
		audioSentinel := []byte{0xFF, 0xFB, 0x90, 0x00, 'M', 'P', '3', 'A', 'U', 'D', 'I', 'O'}
		stream.Write(audioSentinel)

		r := bytes.NewReader(stream.Bytes())
		_, err := ReadID3v2Tags(r)
		e := assertErrClass(t, err, ID3v2ErrorFrameSize)
		if e.Frame != "TPE1" {
			t.Errorf("frame = %q, want TPE1", e.Frame)
		}

		// The tag block was fully buffered up front: even on error the reader
		// must have consumed exactly header(10) + declared body, no audio.
		pos, err := r.Seek(0, io.SeekCurrent)
		if err != nil {
			t.Fatalf("seek: %v", err)
		}
		if want := int64(10 + len(tagBody)); pos != want {
			t.Errorf("cursor = %d, want %d (audio sentinel must remain unread)", pos, want)
		}
	})

	t.Run("v2.4 truncated stream yields truncated class", func(t *testing.T) {
		tagBody := frameV24("TIT2", 0, textPayload("Song"))
		header := id3v2HeaderBytes(4, 0, uint(len(tagBody))+50) // body declares 50 more bytes than exist
		var stream bytes.Buffer
		stream.Write(header)
		stream.Write(tagBody)

		_, err := ReadID3v2Tags(bytes.NewReader(stream.Bytes()))
		assertErrClass(t, err, ID3v2ErrorTruncated)
	})

	t.Run("v2.2 malformed fixed header yields header class", func(t *testing.T) {
		stream := []byte("XXX")
		stream = append(stream, 3, 0, 0, 0, 0, 0, 0)
		_, err := ReadID3v2Tags(bytes.NewReader(stream))
		assertErrClass(t, err, ID3v2ErrorHeader)
	})

	t.Run("v2.4 implausible declared size yields header class", func(t *testing.T) {
		stream := id3v2HeaderBytes(4, 0, maxID3v2TagSize)
		_, err := ReadID3v2Tags(bytes.NewReader(stream))
		assertErrClass(t, err, ID3v2ErrorHeader)
	})
}

func TestReadID3v2AdjacentTags(t *testing.T) {
	// Two adjacent v2.3 tags, each with its own header. The second starts
	// exactly where the first block ends; the parser must peek, parse and merge
	// it, leaving the audio sentinel untouched.
	body1 := frameV23("TIT2", 0, textPayload("First Song"))
	body2 := frameV23("TPE1", 0, textPayload("Second Artist"))

	var stream bytes.Buffer
	stream.Write(id3v2HeaderBytes(3, 0, uint(len(body1))))
	stream.Write(body1)
	stream.Write(id3v2HeaderBytes(3, 0, uint(len(body2))))
	stream.Write(body2)
	audio := []byte{0xFF, 0xFB, 0x90, 0x00, 'A', 'U', 'D', 'I', 'O'}
	stream.Write(audio)

	totalLen := stream.Len()
	r := bytes.NewReader(stream.Bytes())
	m, err := ReadID3v2Tags(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Format() != ID3v2_3 {
		t.Errorf("format = %v, want ID3v2.3", m.Format())
	}
	if m.Title() != "First Song" {
		t.Errorf("title = %q, want First Song", m.Title())
	}
	if m.Artist() != "Second Artist" {
		t.Errorf("artist = %q, want Second Artist (from adjacent tag)", m.Artist())
	}

	pos, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatalf("seek: %v", err)
	}
	wantPos := int64(totalLen - len(audio))
	if pos != wantPos {
		t.Errorf("cursor = %d, want %d (audio sentinel must remain unread)", pos, wantPos)
	}
	rest, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read rest: %v", err)
	}
	if !bytes.Equal(rest, audio) {
		t.Errorf("bytes after tags = %v, want audio sentinel %v", rest, audio)
	}
}

func TestReadID3v2ThreeAdjacentTags(t *testing.T) {
	// Three adjacent tags in a row: the parser must walk all of them and leave
	// the cursor after the third block, audio bytes untouched.
	bodies := [][]byte{
		frameV23("TIT2", 0, textPayload("Song")),
		frameV23("TALB", 0, textPayload("Album")),
		frameV23("TPE1", 0, textPayload("Artist")),
	}
	var stream bytes.Buffer
	totalTagBytes := 0
	for _, b := range bodies {
		stream.Write(id3v2HeaderBytes(3, 0, uint(len(b))))
		stream.Write(b)
		totalTagBytes += 10 + len(b)
	}
	audio := []byte{0xFF, 0xFB, 0x90, 0x00, 'X', 'X', 'X'}
	stream.Write(audio)

	r := bytes.NewReader(stream.Bytes())
	m, err := ReadID3v2Tags(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Title() != "Song" || m.Album() != "Album" || m.Artist() != "Artist" {
		t.Errorf("merged metadata wrong: title=%q album=%q artist=%q",
			m.Title(), m.Album(), m.Artist())
	}
	pos, _ := r.Seek(0, io.SeekCurrent)
	if pos != int64(totalTagBytes) {
		t.Errorf("cursor = %d, want %d", pos, totalTagBytes)
	}
}

func TestReadID3v2AdjacentTagsMergeDuplicateNames(t *testing.T) {
	// Two adjacent tags carrying the same frame id. The merge must keep both
	// values using the numeric-suffix convention, rather than overwriting.
	body1 := frameV23("TIT2", 0, textPayload("First"))
	body2 := frameV23("TIT2", 0, textPayload("Second"))

	var stream bytes.Buffer
	stream.Write(id3v2HeaderBytes(3, 0, uint(len(body1))))
	stream.Write(body1)
	stream.Write(id3v2HeaderBytes(3, 0, uint(len(body2))))
	stream.Write(body2)

	m, err := ReadID3v2Tags(bytes.NewReader(stream.Bytes()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	raw := m.Raw()
	if raw["TIT2"] != "First" {
		t.Errorf("raw[TIT2] = %v, want First", raw["TIT2"])
	}
	if raw["TIT2_0"] != "Second" {
		t.Errorf("raw[TIT2_0] = %v, want Second (duplicate merged with suffix)", raw["TIT2_0"])
	}
}

func TestReadID3v2AdjacentTagsPeekRollback(t *testing.T) {
	// A well-formed tag followed by non-ID3 bytes (audio). The probe must fail
	// and leave the cursor exactly at the tag boundary.
	body := frameV23("TIT2", 0, textPayload("Song"))
	var stream bytes.Buffer
	stream.Write(id3v2HeaderBytes(3, 0, uint(len(body))))
	stream.Write(body)
	audio := make([]byte, 32)
	audio[0] = 0xFF
	stream.Write(audio)

	r := bytes.NewReader(stream.Bytes())
	m, err := ReadID3v2Tags(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Title() != "Song" {
		t.Errorf("title = %q, want Song", m.Title())
	}
	pos, _ := r.Seek(0, io.SeekCurrent)
	if pos != int64(10+len(body)) {
		t.Errorf("cursor = %d, want tag boundary %d", pos, 10+len(body))
	}
}

func TestReadID3v2Unsynchronisation(t *testing.T) {
	// A v2.4 tag with the global unsynchronisation flag. The text frame payload
	// contains an FF 00 sequence that the unsynchroniser must collapse to FF.
	// The frame header's synchsafe size counts the physical (stuffed) bytes;
	// one zero padding byte makes the declared tag body match the physical
	// block after the stuffing byte has been removed logically.
	rawPayload := append([]byte{0}, 'A', 0xFF, 0x00, 'B') // 5 physical, 4 logical bytes
	var frame []byte
	frame = append(frame, "TIT2"...)
	frame = append(frame, synchsafe(uint(len(rawPayload)))...)
	frame = append(frame, 0, 0) // frame flags
	frame = append(frame, rawPayload...)
	tagBody := append(frame, 0x00)                          // padding consumed only as physical bytes
	header := id3v2HeaderBytes(4, 0x80, uint(len(tagBody))) // 0x80 = unsynchronisation

	var stream bytes.Buffer
	stream.Write(header)
	stream.Write(tagBody)

	m, err := ReadID3v2Tags(bytes.NewReader(stream.Bytes()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Title() != "AÿB" { // 0xFF decodes as ISO-8859-1 ÿ
		t.Errorf("title = %q, want AÿB", m.Title())
	}
}

func TestReadID3v2UnsynchronisationBounded(t *testing.T) {
	// With global unsynchronisation on, a frame claiming more physical bytes
	// than the tag block holds must still be stopped by the physical boundary,
	// not by an EOF that could be the MPEG stream.
	rawPayload := []byte{0, 'A', 0xFF, 0x00, 'B'}
	var frame []byte
	frame = append(frame, "TPE1"...)
	frame = append(frame, synchsafe(500)...) // claims 500 physical bytes
	frame = append(frame, 0, 0)
	frame = append(frame, rawPayload...)

	var stream bytes.Buffer
	stream.Write(id3v2HeaderBytes(4, 0x80, uint(len(frame))))
	stream.Write(frame)
	audio := []byte{0xFF, 0xFB, 0x90, 0x00, 'A', 'U', 'D', 'I', 'O'}
	stream.Write(audio)

	r := bytes.NewReader(stream.Bytes())
	_, err := ReadID3v2Tags(r)
	assertErrClass(t, err, ID3v2ErrorFrameSize)

	pos, _ := r.Seek(0, io.SeekCurrent)
	if want := int64(10 + len(frame)); pos != want {
		t.Errorf("cursor = %d, want %d (audio must stay unread)", pos, want)
	}
}

func TestReadID3v2FrameRecordsReflectConsumedBytes(t *testing.T) {
	// Explicitly assert the machine output the task asks for: frame name,
	// consumed bytes and (on failure) error class, across all three versions.
	payload := textPayload("Hi")
	tests := []struct {
		version    Format
		body       []byte
		headerSize uint
	}{
		{ID3v2_2, frameV22("TT2", payload), 6},
		{ID3v2_3, frameV23("TIT2", 0, payload), 10},
		{ID3v2_4, frameV24("TIT2", 0, payload), 10},
	}
	for _, tt := range tests {
		res, err := runStateMachine(t, tt.version, tt.body, false)
		if err != nil {
			t.Fatalf("%v: unexpected error: %v", tt.version, err)
		}
		if len(res.Records) != 1 {
			t.Fatalf("%v: records = %v", tt.version, res.Records)
		}
		rec := res.Records[0]
		if rec.Consumed != tt.headerSize+uint(len(payload)) {
			t.Errorf("%v: consumed = %d, want %d", tt.version, rec.Consumed, tt.headerSize+uint(len(payload)))
		}
		if rec.Size != uint(len(payload)) {
			t.Errorf("%v: size = %d, want %d", tt.version, rec.Size, len(payload))
		}
		if res.Offset != rec.Consumed {
			t.Errorf("%v: final offset %d != consumed %d", tt.version, res.Offset, rec.Consumed)
		}
	}
}

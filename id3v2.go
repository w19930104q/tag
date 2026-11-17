// Copyright 2015, David Howden
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// Genre definitions 0-79 follow the ID3 tag specification of 1999.
// More genres have been successively introduced in later Winamp versions.
// https://en.wikipedia.org/wiki/List_of_ID3v1_genres#Extension_by_Winamp
var id3v2Genres = [...]string{
	"Blues", "Classic Rock", "Country", "Dance", "Disco", "Funk", "Grunge",
	"Hip-Hop", "Jazz", "Metal", "New Age", "Oldies", "Other", "Pop", "R&B",
	"Rap", "Reggae", "Rock", "Techno", "Industrial", "Alternative", "Ska",
	"Death Metal", "Pranks", "Soundtrack", "Euro-Techno", "Ambient",
	"Trip-Hop", "Vocal", "Jazz+Funk", "Fusion", "Trance", "Classical",
	"Instrumental", "Acid", "House", "Game", "Sound Clip", "Gospel",
	"Noise", "AlternRock", "Bass", "Soul", "Punk", "Space", "Meditative",
	"Instrumental Pop", "Instrumental Rock", "Ethnic", "Gothic",
	"Darkwave", "Techno-Industrial", "Electronic", "Pop-Folk",
	"Eurodance", "Dream", "Southern Rock", "Comedy", "Cult", "Gangsta",
	"Top 40", "Christian Rap", "Pop/Funk", "Jungle", "Native American",
	"Cabaret", "New Wave", "Psychedelic", "Rave", "Showtunes", "Trailer",
	"Lo-Fi", "Tribal", "Acid Punk", "Acid Jazz", "Polka", "Retro",
	"Musical", "Rock & Roll", "Hard Rock", "Folk", "Folk-Rock",
	"National Folk", "Swing", "Fast Fusion", "Bebob", "Latin", "Revival",
	"Celtic", "Bluegrass", "Avantgarde", "Gothic Rock", "Progressive Rock",
	"Psychedelic Rock", "Symphonic Rock", "Slow Rock", "Big Band",
	"Chorus", "Easy Listening", "Acoustic", "Humour", "Speech", "Chanson",
	"Opera", "Chamber Music", "Sonata", "Symphony", "Booty Bass", "Primus",
	"Porn Groove", "Satire", "Slow Jam", "Club", "Tango", "Samba",
	"Folklore", "Ballad", "Power Ballad", "Rhythmic Soul", "Freestyle",
	"Duet", "Punk Rock", "Drum Solo", "A capella", "Euro-House", "Dance Hall",
	"Goa", "Drum & Bass", "Club-House", "Hardcore", "Terror", "Indie",
	"Britpop", "Negerpunk", "Polsk Punk", "Beat", "Christian Gangsta Rap",
	"Heavy Metal", "Black Metal", "Crossover", "Contemporary Christian",
	"Christian Rock ", "Merengue", "Salsa", "Thrash Metal", "Anime", "JPop",
	"Synthpop",
	"Christmas", "Art Rock", "Baroque", "Bhangra", "Big Beat", "Breakbeat",
	"Chillout", "Downtempo", "Dub", "EBM", "Eclectic", "Electro",
	"Electroclash", "Emo", "Experimental", "Garage", "Global", "IDM",
	"Illbient", "Industro-Goth", "Jam Band", "Krautrock", "Leftfield", "Lounge",
	"Math Rock", "New Romantic", "Nu-Breakz", "Post-Punk", "Post-Rock", "Psytrance",
	"Shoegaze", "Space Rock", "Trop Rock", "World Music", "Neoclassical", "Audiobook",
	"Audio Theatre", "Neue Deutsche Welle", "Podcast", "Indie Rock", "G-Funk", "Dubstep",
	"Garage Rock", "Psybient",
}

// id3v2Header is a type which represents an ID3v2 tag header.
type id3v2Header struct {
	Version           Format
	Unsynchronisation bool
	ExtendedHeader    bool
	Experimental      bool
	Size              uint
}

// maxID3v2TagSize is the largest tag body (the 7-bit synchsafe declared size)
// that the parser is willing to buffer. The 28-bit synchsafe field tops out at
// 256 MiB; anything at or above this cap is treated as a corrupted header.
const maxID3v2TagSize = 1 << 28

// parseID3v2Header decodes the fixed 10-byte ID3v2 tag header. The returned
// header's Size is the declared body size, excluding these 10 bytes.
func parseID3v2Header(b []byte) (*id3v2Header, error) {
	if len(b) < 10 {
		return nil, fmt.Errorf("expected to read 10 bytes (ID3v2Header): got %d", len(b))
	}
	if string(b[0:3]) != "ID3" {
		return nil, fmt.Errorf("expected to read %q", "ID3")
	}

	var vers Format
	switch uint(b[3]) {
	case 2:
		vers = ID3v2_2
	case 3:
		vers = ID3v2_3
	case 4:
		vers = ID3v2_4
	case 0, 1:
		fallthrough
	default:
		return nil, fmt.Errorf("ID3 version: %v, expected: 2, 3 or 4", uint(b[3]))
	}

	// NB: We ignore b[4] (the revision) as we don't currently rely on it.
	h := &id3v2Header{
		Version:           vers,
		Unsynchronisation: getBit(b[5], 7),
		ExtendedHeader:    getBit(b[5], 6),
		Experimental:      getBit(b[5], 5),
		Size:              uint(get7BitChunkedInt(b[6:10])),
	}
	if h.Size >= maxID3v2TagSize {
		return nil, fmt.Errorf("ID3v2 tag size %d is implausibly large", h.Size)
	}
	return h, nil
}

// skipID3v2ExtendedHeader consumes an extended header from body, which starts at
// the first byte following the 10-byte tag header. It returns the number of
// body bytes consumed. Every read is checked against the available body, so a
// corrupted extended-header length can never run the cursor into audio data.
func skipID3v2ExtendedHeader(body []byte, h *id3v2Header) (uint, error) {
	switch h.Version {
	case ID3v2_3:
		// 4-byte big-endian size; the size field excludes its own 4 bytes.
		if len(body) < 4 {
			return 0, errors.New("truncated ID3v2.3 extended header length")
		}
		n := uint(getInt(body[:4]))
		if n > uint(len(body)-4) {
			return 0, fmt.Errorf("ID3v2.3 extended header size %d exceeds remaining tag body (%d bytes)",
				n, len(body)-4)
		}
		return 4 + n, nil

	case ID3v2_4:
		// 4-byte synchsafe size; the size field includes its own 4 bytes.
		if len(body) < 4 {
			return 0, errors.New("truncated ID3v2.4 extended header length")
		}
		n := uint(get7BitChunkedInt(body[:4]))
		if n < 4 || n > uint(len(body)) {
			return 0, fmt.Errorf("ID3v2.4 extended header size %d is out of range (body: %d bytes)",
				n, len(body))
		}
		return n, nil

	default:
		// Only 2.3 and 2.4 define an extended header.
		return 0, nil
	}
}

// readID3v2Header reads the ID3v2 header (including any extended header) from
// the given io.Reader. offset is the number of bytes consumed from the reader:
// 10 for the fixed header, plus the extended-header bytes when present.
func readID3v2Header(r io.Reader) (h *id3v2Header, offset uint, err error) {
	b, err := readBytes(r, 10)
	if err != nil {
		return nil, 0, fmt.Errorf("expected to read 10 bytes (ID3v2Header): %v", err)
	}
	h, err = parseID3v2Header(b)
	if err != nil {
		return nil, 0, err
	}

	offset = 10
	if !h.ExtendedHeader {
		return h, offset, nil
	}

	// Only the length prefix needs to be read first; the remainder is then
	// consumed in one bounded read.
	var extLen uint
	switch h.Version {
	case ID3v2_3:
		prefix, perr := readBytes(r, 4)
		if perr != nil {
			return nil, 0, fmt.Errorf("expected to read 4 bytes (ID3v23 extended header len): %v", perr)
		}
		extLen = 4 + uint(getInt(prefix))
	case ID3v2_4:
		prefix, perr := readBytes(r, 4)
		if perr != nil {
			return nil, 0, fmt.Errorf("expected to read 4 bytes (ID3v24 extended header len): %v", perr)
		}
		extLen = uint(get7BitChunkedInt(prefix))
	default:
		return h, offset, nil
	}

	if extLen > 4 {
		if _, err = readBytes(r, extLen-4); err != nil {
			return nil, 0, fmt.Errorf("expected to read %d bytes (skip extended header): %v", extLen-4, err)
		}
	}
	offset += extLen
	return h, offset, nil
}

// id3v2FrameFlags is a type which represents the flags which can be set on an ID3v2 frame.
type id3v2FrameFlags struct {
	// Message (ID3 2.3.0 and 2.4.0)
	TagAlterPreservation  bool
	FileAlterPreservation bool
	ReadOnly              bool

	// Format (ID3 2.3.0 and 2.4.0)
	Compression   bool
	Encryption    bool
	GroupIdentity bool
	// ID3 2.4.0 only (see http://id3.org/id3v2.4.0-structure sec 4.1)
	Unsynchronisation   bool
	DataLengthIndicator bool
}

func readID3v23FrameFlags(r io.Reader) (*id3v2FrameFlags, error) {
	b, err := readBytes(r, 2)
	if err != nil {
		return nil, err
	}

	msg := b[0]
	fmt := b[1]

	return &id3v2FrameFlags{
		TagAlterPreservation:  getBit(msg, 7),
		FileAlterPreservation: getBit(msg, 6),
		ReadOnly:              getBit(msg, 5),
		Compression:           getBit(fmt, 7),
		Encryption:            getBit(fmt, 6),
		GroupIdentity:         getBit(fmt, 5),
	}, nil
}

func readID3v24FrameFlags(r io.Reader) (*id3v2FrameFlags, error) {
	b, err := readBytes(r, 2)
	if err != nil {
		return nil, err
	}

	msg := b[0]
	fmt := b[1]

	return &id3v2FrameFlags{
		TagAlterPreservation:  getBit(msg, 6),
		FileAlterPreservation: getBit(msg, 5),
		ReadOnly:              getBit(msg, 4),
		GroupIdentity:         getBit(fmt, 6),
		Compression:           getBit(fmt, 3),
		Encryption:            getBit(fmt, 2),
		Unsynchronisation:     getBit(fmt, 1),
		DataLengthIndicator:   getBit(fmt, 0),
	}, nil

}

func readID3v2_2FrameHeader(r io.Reader) (name string, size uint, headerSize uint, err error) {
	name, err = readString(r, 3)
	if err != nil {
		return
	}
	size, err = readUint(r, 3)
	if err != nil {
		return
	}
	headerSize = 6
	return
}

func readID3v2_3FrameHeader(r io.Reader) (name string, size uint, headerSize uint, err error) {
	name, err = readString(r, 4)
	if err != nil {
		return
	}
	size, err = readUint(r, 4)
	if err != nil {
		return
	}
	headerSize = 8
	return
}

func readID3v2_4FrameHeader(r io.Reader) (name string, size uint, headerSize uint, err error) {
	name, err = readString(r, 4)
	if err != nil {
		return
	}
	size, err = read7BitChunkedUint(r, 4)
	if err != nil {
		return
	}
	headerSize = 8
	return
}

// ID3v2ErrorClass categorises the reason an ID3v2 parse stopped, so callers
// (and tests) can distinguish a corrupted tag from a truncated stream without
// matching on error message text.
type ID3v2ErrorClass int

const (
	// ID3v2ErrorHeader means the fixed tag header (or declared tag size) was
	// malformed.
	ID3v2ErrorHeader ID3v2ErrorClass = iota
	// ID3v2ErrorExtendedHeader means the extended header length was truncated
	// or pointed outside the tag body.
	ID3v2ErrorExtendedHeader
	// ID3v2ErrorTruncated means the underlying stream ended before the number
	// of bytes declared by the tag header.
	ID3v2ErrorTruncated
	// ID3v2ErrorFrameHeader means a frame header was cut short by the end of
	// the tag body, or contained an all-zero name in the middle of the tag.
	ID3v2ErrorFrameHeader
	// ID3v2ErrorFrameSize means the frame's declared size does not fit inside
	// the remaining tag body (the classic "frame size runs into audio data"
	// corruption).
	ID3v2ErrorFrameSize
	// ID3v2ErrorFrameData means the frame was fully bounded but its payload
	// could not be decoded.
	ID3v2ErrorFrameData
)

// String returns a stable, human-readable identifier for the error class.
func (c ID3v2ErrorClass) String() string {
	switch c {
	case ID3v2ErrorHeader:
		return "header"
	case ID3v2ErrorExtendedHeader:
		return "extended-header"
	case ID3v2ErrorTruncated:
		return "truncated"
	case ID3v2ErrorFrameHeader:
		return "frame-header"
	case ID3v2ErrorFrameSize:
		return "frame-size"
	case ID3v2ErrorFrameData:
		return "frame-data"
	}
	return "unknown"
}

// ID3v2Error wraps a parse failure with the byte offset (relative to the start
// of the tag body) at which parsing stopped, the frame being processed (if
// any) and a machine-readable error class.
type ID3v2Error struct {
	Class  ID3v2ErrorClass
	Offset uint
	Frame  string
	Err    error
}

func (e *ID3v2Error) Error() string {
	if e.Frame != "" {
		return fmt.Sprintf("id3v2: %s error at body offset %d (frame %q): %v",
			e.Class, e.Offset, e.Frame, e.Err)
	}
	return fmt.Sprintf("id3v2: %s error at body offset %d: %v",
		e.Class, e.Offset, e.Err)
}

func (e *ID3v2Error) Unwrap() error { return e.Err }

func id3v2Err(class ID3v2ErrorClass, offset uint, frame string, format string, args ...interface{}) *ID3v2Error {
	return &ID3v2Error{
		Class:  class,
		Offset: offset,
		Frame:  frame,
		Err:    fmt.Errorf(format, args...),
	}
}

// id3v2StopReason explains why the frame state machine terminated cleanly
// (without an error).
type id3v2StopReason int

const (
	// stopEndOfTag means the cursor consumed exactly the declared tag body.
	stopEndOfTag id3v2StopReason = iota
	// stopPadding means a zero-size frame (or an all-zero padding region) was
	// encountered; any remaining body bytes are padding.
	stopPadding
)

// ID3v2FrameRecord is one state-machine transition: a frame that was accepted,
// the size its header declared, and the number of tag-body bytes it consumed
// (header + flags + payload, after compression/encryption adjustments).
type ID3v2FrameRecord struct {
	Name     string
	Size     uint
	Consumed uint
}

// id3v2ParseResult is the observable end state of one tag's frame state
// machine. Offset is always the final cursor position inside the tag body, so
// the caller knows exactly how much of the bounded buffer was touched.
type id3v2ParseResult struct {
	Frames  map[string]interface{}
	Records []ID3v2FrameRecord
	Offset  uint
	Reason  id3v2StopReason
}

// frameHeaderSize returns the on-the-wire frame header size for a version.
func frameHeaderSize(v Format) uint {
	if v == ID3v2_2 {
		return 6 // 3-byte id + 3-byte size
	}
	return 10 // 4-byte id + 4-byte size + 2-byte flags
}

// isAllZero reports whether b consists entirely of zero bytes (padding).
func isAllZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

// readID3v2Frames runs the bounded frame state machine. r is positioned at the
// first frame byte and is backed by a reader that cannot serve bytes beyond
// the physical tag block; regionLen is the number of physical bytes from that
// position to the end of the tag body; base is the absolute body offset of
// that position (the extended-header size, used for error/result offsets);
// body is the full physical tag body, used to inspect padding bytes. When the
// tag uses global unsynchronisation, r removes the stuffing bytes on the fly,
// while every boundary check still uses physical, on-the-wire sizes.
//
// The machine never reads beyond the tag block: every frame header and payload
// is range-checked against the remaining physical bytes before it is touched,
// so a bad frame size can never pull audio data into the parser and a padding
// region can never cause an unbounded skip.
func readID3v2Frames(r io.Reader, regionLen, base uint, h *id3v2Header, body []byte) (*id3v2ParseResult, error) {
	result := &id3v2ParseResult{Frames: make(map[string]interface{}), Offset: base}
	headerLen := frameHeaderSize(h.Version)

	// pos is the physical cursor relative to the frame region start.
	pos := uint(0)

	for {
		// State 1: boundary. No room for another frame header: either a clean
		// end-of-tag or a truncated/corrupted trailing region.
		remaining := regionLen - pos
		if remaining == 0 {
			result.Offset = base + pos
			result.Reason = stopEndOfTag
			return result, nil
		}
		if remaining < headerLen {
			tail := body[base+pos:]
			if isAllZero(tail) {
				result.Offset = base + pos
				result.Reason = stopPadding
				return result, nil
			}
			return nil, id3v2Err(ID3v2ErrorFrameHeader, base+pos, "",
				"%d trailing byte(s) cannot hold a frame header (%d bytes)", remaining, headerLen)
		}

		frameStart := pos

		// State 2: frame header, read from the bounded reader only.
		var (
			name       string
			size       uint
			flags      *id3v2FrameFlags
			frameHSize uint
			err        error
		)
		switch h.Version {
		case ID3v2_2:
			name, size, frameHSize, err = readID3v2_2FrameHeader(r)
		case ID3v2_3:
			name, size, frameHSize, err = readID3v2_3FrameHeader(r)
			if err != nil {
				return nil, id3v2Err(ID3v2ErrorFrameHeader, base+frameStart, "",
					"reading frame header: %v", err)
			}
			flags, err = readID3v23FrameFlags(r)
			frameHSize += 2
		case ID3v2_4:
			name, size, frameHSize, err = readID3v2_4FrameHeader(r)
			if err != nil {
				return nil, id3v2Err(ID3v2ErrorFrameHeader, base+frameStart, "",
					"reading frame header: %v", err)
			}
			flags, err = readID3v24FrameFlags(r)
			frameHSize += 2
		}
		if err != nil {
			return nil, id3v2Err(ID3v2ErrorFrameHeader, base+frameStart, "",
				"reading frame header: %v", err)
		}

		// State 3: padding. A zero-size frame marks the start of the padding
		// zone; an all-zero frame id at the current cursor is the same thing.
		if size == 0 || isAllZero([]byte(name)) {
			if isAllZero(body[base+frameStart:]) {
				result.Offset = base + frameStart
				result.Reason = stopPadding
				return result, nil
			}
			return nil, id3v2Err(ID3v2ErrorFrameHeader, base+frameStart, name,
				"zero-size frame is not followed by zero padding")
		}

		// State 4: boundary check. The declared frame must fit inside the
		// bytes the tag header actually gave us, whatever the frame id claims
		// to be. An unknown frame id that overflows is tolerated as padding
		// (per the ID3 compliance guidance); a spec-defined frame that
		// overflows is hard corruption, because trusting it would read into
		// the MPEG audio stream.
		if frameHSize+size > remaining {
			if !validID3Frame(h.Version, name) {
				result.Offset = base + frameStart
				result.Reason = stopPadding
				return result, nil
			}
			return nil, id3v2Err(ID3v2ErrorFrameSize, base+frameStart, name,
				"frame needs %d header+data bytes but only %d remain in tag body",
				frameHSize+size, remaining)
		}

		// State 5: in-header payload adjustments (compression / data length
		// indicator / encryption), all bounded by the same range check.
		payloadSize := size
		if flags != nil {
			if flags.Compression {
				switch h.Version {
				case ID3v2_3:
					// No data length indicator defined; 4 extra bytes precede
					// the compressed payload.
					if _, err = read7BitChunkedUint(r, 4); err != nil {
						return nil, id3v2Err(ID3v2ErrorFrameHeader, base+pos, name,
							"reading compression indicator: %v", err)
					}
					if payloadSize < 4 {
						return nil, id3v2Err(ID3v2ErrorFrameSize, base+frameStart, name,
							"compression indicator underflows frame size %d", payloadSize)
					}
					payloadSize -= 4

				case ID3v2_4:
					// Must have a data length indicator (to give the size) if compression is enabled.
					if !flags.DataLengthIndicator {
						return nil, id3v2Err(ID3v2ErrorFrameData, base+frameStart, name,
							"compression without data length indicator")
					}

				default:
					return nil, id3v2Err(ID3v2ErrorFrameData, base+frameStart, name,
						"unsupported compression flag used in %v", h.Version)
				}
			}

			if flags.DataLengthIndicator {
				if h.Version == ID3v2_3 {
					return nil, id3v2Err(ID3v2ErrorFrameData, base+frameStart, name,
						"data length indicator set but not defined for %v", ID3v2_3)
				}

				payloadSize, err = read7BitChunkedUint(r, 4)
				if err != nil {
					return nil, id3v2Err(ID3v2ErrorFrameHeader, base+pos, name,
						"reading data length indicator: %v", err)
				}
			}

			if flags.Encryption {
				if _, err = readBytes(r, 1); err != nil { // 1 byte of encryption method
					return nil, id3v2Err(ID3v2ErrorFrameHeader, base+pos, name,
						"reading encryption method: %v", err)
				}
				if payloadSize == 0 {
					return nil, id3v2Err(ID3v2ErrorFrameSize, base+frameStart, name,
						"encryption method byte underflows frame size")
				}
				payloadSize--
			}

			// Adjustments must not claim more bytes than the frame header did.
			if payloadSize > size {
				return nil, id3v2Err(ID3v2ErrorFrameSize, base+frameStart, name,
					"adjusted payload size %d exceeds declared frame size %d", payloadSize, size)
			}
		}

		b, err := readBytes(r, payloadSize)
		if err != nil {
			return nil, id3v2Err(ID3v2ErrorFrameSize, base+frameStart, name,
				"reading frame payload: %v", err)
		}

		// State 6: commit the transition only after the whole frame has been
		// consumed, so the cursor is always at a frame boundary.
		pos = frameStart + frameHSize + size
		result.Offset = base + pos
		result.Records = append(result.Records, ID3v2FrameRecord{
			Name:     name,
			Size:     size,
			Consumed: frameHSize + size,
		})

		// There can be multiple tag with the same name. Append a number to the
		// name if there is more than one.
		rawName := name
		if _, ok := result.Frames[rawName]; ok {
			for i := 0; ok; i++ {
				rawName = name + "_" + strconv.Itoa(i)
				_, ok = result.Frames[rawName]
			}
		}

		switch {
		case name == "TXXX" || name == "TXX":
			t, err := readTextWithDescrFrame(b, false, true) // no lang, but enc
			if err != nil {
				return nil, id3v2Err(ID3v2ErrorFrameData, base+frameStart, name,
					"decoding text frame: %v", err)
			}
			result.Frames[rawName] = t

		case name[0] == 'T':
			txt, err := readTFrame(b)
			if err != nil {
				return nil, id3v2Err(ID3v2ErrorFrameData, base+frameStart, name,
					"decoding text frame: %v", err)
			}
			result.Frames[rawName] = txt

		case name == "UFID" || name == "UFI":
			t, err := readUFID(b)
			if err != nil {
				return nil, id3v2Err(ID3v2ErrorFrameData, base+frameStart, name,
					"decoding UFID frame: %v", err)
			}
			result.Frames[rawName] = t

		case name == "WXXX" || name == "WXX":
			t, err := readTextWithDescrFrame(b, false, false) // no lang, no enc
			if err != nil {
				return nil, id3v2Err(ID3v2ErrorFrameData, base+frameStart, name,
					"decoding URL frame: %v", err)
			}
			result.Frames[rawName] = t

		case name[0] == 'W':
			txt, err := readWFrame(b)
			if err != nil {
				return nil, id3v2Err(ID3v2ErrorFrameData, base+frameStart, name,
					"decoding URL frame: %v", err)
			}
			result.Frames[rawName] = txt

		case name == "COMM" || name == "COM" || name == "USLT" || name == "ULT":
			t, err := readTextWithDescrFrame(b, true, true) // both lang and enc
			if err != nil {
				return nil, id3v2Err(ID3v2ErrorFrameData, base+frameStart, name,
					"could not read %q (%q): %v", name, rawName, err)
			}
			result.Frames[rawName] = t

		case name == "APIC":
			p, err := readAPICFrame(b)
			if err != nil {
				return nil, id3v2Err(ID3v2ErrorFrameData, base+frameStart, name,
					"decoding APIC frame: %v", err)
			}
			result.Frames[rawName] = p

		case name == "PIC":
			p, err := readPICFrame(b)
			if err != nil {
				return nil, id3v2Err(ID3v2ErrorFrameData, base+frameStart, name,
					"decoding PIC frame: %v", err)
			}
			result.Frames[rawName] = p

		default:
			result.Frames[rawName] = b
		}
	}
}

type unsynchroniser struct {
	io.Reader
	ff bool
}

// filter io.Reader which skip the Unsynchronisation bytes
func (r *unsynchroniser) Read(p []byte) (int, error) {
	b := make([]byte, 1)
	i := 0
	for i < len(p) {
		if n, err := r.Reader.Read(b); err != nil || n == 0 {
			return i, err
		}
		if r.ff && b[0] == 0x00 {
			r.ff = false
			continue
		}
		p[i] = b[0]
		i++
		r.ff = (b[0] == 0xFF)
	}
	return i, nil
}

// readTagBlock reads exactly size bytes from r, returning a classified
// ID3v2Error (ID3v2ErrorTruncated) when the stream ends early.
func readTagBlock(r io.Reader, size uint) ([]byte, error) {
	b, err := readBytes(r, size)
	if err != nil {
		return nil, id3v2Err(ID3v2ErrorTruncated, 0, "",
			"expected %d tag body bytes: %v", size, err)
	}
	return b, nil
}

// parseID3v2TagBody runs the frame state machine over one tag block that has
// already been read into body (exactly header.Size physical bytes). It does
// not touch the underlying stream, so the caller stays in full control of the
// cursor and can position it at the next tag boundary itself.
func parseID3v2TagBody(body []byte, h *id3v2Header) (*id3v2ParseResult, error) {
	base := uint(0)
	if h.ExtendedHeader {
		n, err := skipID3v2ExtendedHeader(body, h)
		if err != nil {
			return nil, id3v2Err(ID3v2ErrorExtendedHeader, 0, "", "%v", err)
		}
		base = n
	}

	// The frame reader is hard-bounded to the physical tag block; even a
	// corrupted frame size cannot make it serve audio bytes.
	var frameReader io.Reader = io.LimitReader(bytes.NewReader(body[base:]), int64(len(body)-int(base)))
	if h.Unsynchronisation {
		// Global unsynchronisation removes stuffing on the fly. Boundary
		// checks still use the physical sizes, and the LimitReader keeps the
		// underlying consumption inside the tag block.
		frameReader = &unsynchroniser{Reader: frameReader}
	}

	return readID3v2Frames(frameReader, uint(len(body))-base, base, h, body)
}

// maxAdjacentID3v2Tags bounds how many adjacent tag blocks one call will walk,
// so a chain of crafted zero-size tags cannot make the loop unbounded.
const maxAdjacentID3v2Tags = 16

// ReadID3v2Tags parses ID3v2.{2,3,4} tags from the io.ReadSeeker into a Metadata, returning
// a non-nil error on failure.
//
// Parsing is driven by an explicit, bounded state machine: each tag is read as
// a fixed-size block and frame cursors are checked against that block, so a
// corrupted frame size or padding region can never reach the MPEG audio data
// or loop on zero bytes. When several ID3v2 tags are adjacent (a common
// encoder artifact), each tag is parsed in turn and their frames merged; the
// returned cursor always sits at the first byte after the last parsed tag.
func ReadID3v2Tags(r io.ReadSeeker) (Metadata, error) {
	frames := make(map[string]interface{})
	var firstHeader *id3v2Header

	// h is the header of the tag about to be parsed. The first read happens
	// unconditionally at the current cursor; subsequent iterations only run
	// after a successful probe proved another ID3 header sits there.
	var h *id3v2Header

	for tagIndex := 0; ; tagIndex++ {
		if tagIndex >= maxAdjacentID3v2Tags {
			return nil, id3v2Err(ID3v2ErrorHeader, 0, "",
				"more than %d adjacent ID3v2 tags", maxAdjacentID3v2Tags)
		}
		headerStart, serr := r.Seek(0, io.SeekCurrent)
		if serr != nil {
			return nil, id3v2Err(ID3v2ErrorHeader, 0, "", "recording cursor: %v", serr)
		}
		headerBytes, err := readBytes(r, 10)
		if err != nil {
			if tagIndex > 0 {
				// A previous tag was already parsed: trailing short data is the
				// audio stream, not a malformed tag. Restore the boundary.
				if _, serr := r.Seek(headerStart, io.SeekStart); serr != nil {
					return nil, id3v2Err(ID3v2ErrorHeader, 0, "",
						"restoring cursor: %v", serr)
				}
				break
			}
			return nil, id3v2Err(ID3v2ErrorHeader, 0, "",
				"expected to read 10 bytes (ID3v2Header): %v", err)
		}
		h, err = parseID3v2Header(headerBytes)
		if err != nil {
			if tagIndex > 0 {
				// Not another ID3 tag: restore the cursor to the boundary and stop.
				if _, serr := r.Seek(headerStart, io.SeekStart); serr != nil {
					return nil, id3v2Err(ID3v2ErrorHeader, 0, "",
						"restoring cursor after probe: %v", serr)
				}
				break
			}
			return nil, id3v2Err(ID3v2ErrorHeader, 0, "", "%v", err)
		}

		body, err := readTagBlock(r, h.Size)
		if err != nil {
			return nil, err
		}

		result, err := parseID3v2TagBody(body, h)
		if err != nil {
			return nil, err
		}

		for k, v := range result.Frames {
			// Merge adjacent tags while preserving the "duplicate name gets a
			// numeric suffix" convention.
			if _, ok := frames[k]; !ok {
				frames[k] = v
				continue
			}
			rawName := k
			for i := 0; ; i++ {
				candidate := k + "_" + strconv.Itoa(i)
				if _, taken := frames[candidate]; !taken {
					rawName = candidate
					break
				}
			}
			frames[rawName] = v
		}

		if tagIndex == 0 {
			firstHeader = h
		}

		// Probe for another adjacent ID3v2 tag without consuming any bytes:
		// seek to the boundary, read the header, then seek back. A read that
		// returns fewer than 10 bytes (e.g. the short MPEG audio tail) must
		// still leave the cursor exactly at the tag boundary, which a
		// consuming read plus a fixed seek-back cannot guarantee.
		start, serr := r.Seek(0, io.SeekCurrent)
		if serr != nil {
			return nil, id3v2Err(ID3v2ErrorHeader, 0, "", "recording cursor: %v", serr)
		}
		peek, perr := readBytes(r, 10)
		hasNext := false
		if perr == nil {
			if _, nerr := parseID3v2Header(peek); nerr == nil {
				hasNext = true
			}
		}
		if _, serr := r.Seek(start, io.SeekStart); serr != nil {
			return nil, id3v2Err(ID3v2ErrorHeader, 0, "",
				"restoring cursor after probe: %v", serr)
		}
		if !hasNext {
			break
		}
	}

	if firstHeader == nil {
		return nil, id3v2Err(ID3v2ErrorHeader, 0, "", "no ID3v2 tag parsed")
	}
	return metadataID3v2{header: firstHeader, frames: frames}, nil
}

var id3v2genreRe = regexp.MustCompile(`(.*[^(]|.* |^)\(([0-9]+)\) *(.*)$`)

//  id3v2genre parse a id3v2 genre tag and expand the numeric genres
func id3v2genre(genre string) string {
	c := true
	for c {
		orig := genre
		if match := id3v2genreRe.FindStringSubmatch(genre); len(match) > 0 {
			if genreID, err := strconv.Atoi(match[2]); err == nil {
				if genreID < len(id3v2Genres) {
					genre = id3v2Genres[genreID]
					if match[1] != "" {
						genre = strings.TrimSpace(match[1]) + " " + genre
					}
					if match[3] != "" {
						genre = genre + " " + match[3]
					}
				}
			}
		}
		c = (orig != genre)
	}
	return strings.Replace(genre, "((", "(", -1)
}

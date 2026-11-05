package bot

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
)

// oggOpusReader returns the raw Opus packets of an Ogg Opus stream, skipping the
// OpusHead and OpusTags header packets - https://www.rfc-editor.org/rfc/rfc7845
type oggOpusReader struct {
	r       *bufio.Reader
	queue   [][]byte
	partial []byte
	headers int
}

func newOggOpusReader(r io.Reader) *oggOpusReader {
	return &oggOpusReader{r: bufio.NewReader(r)}
}

func (o *oggOpusReader) NextPacket() ([]byte, error) {
	for {
		for len(o.queue) > 0 {
			pkt := o.queue[0]
			o.queue = o.queue[1:]
			if o.headers < 2 {
				if err := o.checkHeader(pkt); err != nil {
					return nil, err
				}
				o.headers++
				continue
			}
			return pkt, nil
		}
		if err := o.readPage(); err != nil {
			return nil, err
		}
	}
}

func (o *oggOpusReader) checkHeader(pkt []byte) error {
	want := []byte("OpusHead")
	if o.headers == 1 {
		want = []byte("OpusTags")
	}
	if !bytes.HasPrefix(pkt, want) {
		return fmt.Errorf("expected %s packet, stream is not Ogg Opus", want)
	}
	return nil
}

func (o *oggOpusReader) readPage() error {
	var hdr [27]byte
	if _, err := io.ReadFull(o.r, hdr[:]); err != nil {
		return err
	}
	if !bytes.Equal(hdr[:4], []byte("OggS")) {
		return errors.New("invalid ogg page capture pattern")
	}
	segTable := make([]byte, hdr[26])
	if _, err := io.ReadFull(o.r, segTable); err != nil {
		return err
	}
	for _, size := range segTable {
		seg := make([]byte, size)
		if _, err := io.ReadFull(o.r, seg); err != nil {
			return err
		}
		o.partial = append(o.partial, seg...)
		if size < 255 {
			o.queue = append(o.queue, o.partial)
			o.partial = nil
		}
	}
	return nil
}

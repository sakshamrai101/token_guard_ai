package sse

import "strings"

type Parser struct {
	lineBuf     strings.Builder
	pendingData []string
}

func NewParser() *Parser {
	return &Parser{}
}

func (p *Parser) Feed(chunk []byte) []Event {
	if len(chunk) > 0 {
		_, _ = p.lineBuf.Write(chunk)
	}
	return p.drainLines(false)
}

func (p *Parser) Flush() []Event {
	events := p.drainLines(true)
	if p.lineBuf.Len() > 0 {
		line := strings.TrimSuffix(p.lineBuf.String(), "\r")
		p.lineBuf.Reset()
		events = append(events, p.processLine(line)...)
	}
	events = append(events, p.flushData()...)
	return events
}

func (p *Parser) drainLines(flushRemainder bool) []Event {
	var events []Event
	for {
		s := p.lineBuf.String()
		idx := strings.IndexByte(s, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimSuffix(s[:idx], "\r")
		remainder := s[idx+1:]
		p.lineBuf.Reset()
		_, _ = p.lineBuf.WriteString(remainder)
		events = append(events, p.processLine(line)...)
	}
	if flushRemainder && p.lineBuf.Len() > 0 {
		line := strings.TrimSuffix(p.lineBuf.String(), "\r")
		p.lineBuf.Reset()
		events = append(events, p.processLine(line)...)
		events = append(events, p.flushData()...)
	}
	return events
}

func (p *Parser) processLine(line string) []Event {
	if line == "" {
		return p.flushData()
	}
	if strings.HasPrefix(line, ":") {
		return nil
	}
	if strings.HasPrefix(line, "data:") {
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			return []Event{{Done: true}}
		}
		p.pendingData = append(p.pendingData, data)
	}
	return nil
}

func (p *Parser) flushData() []Event {
	if len(p.pendingData) == 0 {
		return nil
	}
	data := strings.Join(p.pendingData, "\n")
	p.pendingData = nil
	return []Event{{Data: data}}
}

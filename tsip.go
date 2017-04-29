package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"os"
	"time"
)

const MSG_MAX_LEN = 256

const (
	EXPECT_START = iota
	EXPECT_DONE
	IN_MESSAGE
)

type handleable interface {
	Handle()
}

type Action struct {
	MatchSequence []byte
	decodeInto    handleable
}

type trimbleCmd interface {
	PacketID() []byte
}

type GetSoftwareVersionCmd struct {
	// NODATA - this command is empty
}

type GetSignalLevelCmd struct {
	// NODATA - this command is empty
}

// Note:  The Thunderbolt E appears to send year - 2000 for the App/GPS info, not
// Year - 1900.  (But the packet struct here is written per the spec).
// Also, Trimble refers to the software version as 2.10, but it gets
// reported here - again, per the spec, as 10.2.  Beats me.
type SoftwareVersionPacket struct {
	AppMajor        uint8
	AppMinor        uint8
	AppMonth        uint8
	AppDay          uint8
	AppYearFrom2000 uint8
	GPSMajor        uint8
	GPSMinor        uint8
	GPSMonth        uint8
	GPSDay          uint8
	GPSYearFrom2000 uint8
}

func (c *GetSoftwareVersionCmd) PacketID() []byte { return []byte{0x1f} }

func (c *GetSignalLevelCmd) PacketID() []byte { return []byte{0x27} }

type PrimaryTimingPacket struct {
	Subcode    uint8
	TimeOfWeek uint32
	WeekNumber uint16
	UTCOffset  int16
	TimingFlag byte
	Seconds    uint8
	Minutes    uint8
	Hours      uint8
	DayOfMonth uint8
	Month      uint8
	Year       uint16
}

type SecondaryTimingPacket struct {
	Subcode              uint8
	ReceiverMode         uint8
	DiscipliningMode     uint8
	SelfSurveyProgress   uint8
	HoldoverDuration     uint32
	CriticalAlarms       uint16
	MinorAlarms          uint16
	GPSDecodeStatus      uint8
	DiscipliningActivity uint8
	SpareStatus1         uint8
	SpareStatus2         uint8
	PPSOffset            float32
	TenMhzOffset         float32
	DACValue             uint32
	DACVoltage           float32
	Temperature          float32
	Latitude             float64
	Longitude            float64
	Altitude             float64
	Spare                int64
}

type PPSCharacteristicsPacket struct {
	Subcode         uint8
	PPSOutputEnable uint8
	Reserved        uint8
	PPSPolarity     uint8
	PPSOffset       float64
	BiasThreshold   float32
}

func (p *SecondaryTimingPacket) Handle() {
	fmt.Printf("Secondary packet:  RCV %d, DIS %d, SUR %d PPS-OFFSET: %f CriticalAlarm: %x MinorAlarm: %x DecodeStatus: %x Temp: %f Lat: %f Long: %f Alt: %f \n",
		p.ReceiverMode, p.DiscipliningMode, p.SelfSurveyProgress, p.PPSOffset, p.CriticalAlarms, p.MinorAlarms, p.GPSDecodeStatus, p.Temperature, p.Latitude*180.0/math.Pi, p.Longitude*180.0/math.Pi, p.Altitude)
}

func (p *PrimaryTimingPacket) Handle() {
	fmt.Printf("Primary Timing Packet:  %d/%d/%d %d:%d:%d  (GPS offset %d)\n", p.Year, p.Month, p.DayOfMonth, p.Hours, p.Minutes, p.Seconds, p.UTCOffset)

}

func (p *SoftwareVersionPacket) Handle() {
	fmt.Printf("Software Version Response:  App: %d.%d %d/%d/%d  GPS: %d.%d %d/%d/%d\n",
		p.AppMajor, p.AppMinor, int(p.AppYearFrom2000)+2000, p.AppMonth, p.AppDay,
		p.GPSMajor, p.GPSMinor, int(p.GPSYearFrom2000)+2000, p.GPSMonth, p.GPSDay)
}

func (p *PPSCharacteristicsPacket) Handle() {
	fmt.Printf("PPS Characteristics packet: Output-enable %d, Polarity %d, PPS Offset: %f, Bias Threshold: %f\n",
		p.PPSOutputEnable, p.PPSPolarity, p.PPSOffset, p.BiasThreshold)
}

var actions []Action

func init() {
	// HUMAN:  The parser requires that you list these in descending
	// order of MatchSequence length.
	actions = []Action{
		Action{[]byte{0x8f, 0xab}, &PrimaryTimingPacket{}},
		Action{[]byte{0x8f, 0xac}, &SecondaryTimingPacket{}},
		Action{[]byte{0x8f, 0x4a}, &PPSCharacteristicsPacket{}},
		Action{[]byte{0x45}, &SoftwareVersionPacket{}},
	}
}

func sendCmd(c trimbleCmd) {
	buf := new(bytes.Buffer)
	// DLE.id.{cmd bytes}.DLE.ETX
	_ = binary.Write(buf, binary.BigEndian, c)
	bufBytes := buf.Bytes()
	bufNew := bytes.Replace(bufBytes, []byte{0x10}, []byte{0x10, 0x10}, -1)
	buf.Reset()
	buf.WriteByte(0x10)
	buf.Write(c.PacketID())
	buf.Write(bufNew)
	buf.Write([]byte{0x10, 0x03})

	buf.WriteTo(theConn)
}

func handleMsg(msg []byte) {
	var p handleable
	handled := false

	for _, a := range actions {
		alen := len(a.MatchSequence)
		if bytes.Equal(msg[0:alen], a.MatchSequence) {
			p = a.decodeInto
			handled = true
			break
		}
	}

	if handled {
		r := bytes.NewReader(msg[1:])
		binary.Read(r, binary.BigEndian, p)
		p.Handle()
	} else {
		handleVariableMsg(msg)
	}
}

type NumberAndLevel struct {
	PRNNumber   uint8
	SignalLevel float32
}

func (p *NumberAndLevel) Handle() {
	level := int(p.SignalLevel)
	if level > 0 {
		fmt.Printf("Satellite Signal Report:  PRN %d Signal %d\n",
			p.PRNNumber, level)
	}
}

func handleSatelliteSignalReport(msg []byte) {
	var p NumberAndLevel

	count := int(msg[1])
	r := bytes.NewReader(msg[2:])
	i := 0
	for i < count {
		binary.Read(r, binary.BigEndian, &p)
		p.Handle()
		i++
	}
}

func handleVariableMsg(msg []byte) {
	if msg[0] == 0x47 { // Satellite Signal Report
		handleSatelliteSignalReport(msg)
	} else {
		fmt.Printf("Unknown packet type: %x (%x)\n", msg[0], msg[1])
	}
}

var theConn net.Conn // xxx, fix me...

func main() {
	if len(os.Args) != 3 {
		fmt.Printf("Usage: %s address port\n", os.Args[0])
		os.Exit(1)
	}
	address := os.Args[1]
	port := os.Args[2]
	destination := string(address) + ":" + string(port)
	fmt.Printf("connecting to serial server %s\n", destination)
	conn, err := net.Dial("tcp", destination)
	if err != nil {
		fmt.Println("could not connect:", err)
		return
	}
	theConn = conn
	br := bufio.NewReader(conn)
	// Find a start of message
	for {
		c, _ := br.ReadByte()
		if c == 0x10 {
			c, _ := br.ReadByte()
			if c == 0x3 {
				break
			}
		}
	}
	state := 0
	// XXX - demo:  Grab the software version command after running for a second.
	go func() {
		time.Sleep(time.Second)
		fmt.Println("Sending GetSoftwareVersionCmd")
		sendCmd(&GetSoftwareVersionCmd{})
	}()

	go func() {
		time.Sleep(time.Second)
		fmt.Println("Sending GetSignalLevelCmd")
		sendCmd(&GetSignalLevelCmd{})
	}()

	var msg [MSG_MAX_LEN]byte
	msgptr := 0
	for {
		c, _ := br.ReadByte()
		if c == 0x10 {
			// Attempt to de-stuff DLEs if they're in message data
			nextbytes, _ := br.Peek(1)
			if nextbytes[0] == 0x10 {
				c, _ = br.ReadByte()
			} else {
				if state == EXPECT_START {
					msgptr = 0
					state = IN_MESSAGE
					continue
				} else {
					state = EXPECT_DONE
					continue
				}
			}
		}

		if state == EXPECT_DONE {
			if c != 0x03 {
				fmt.Println("Error:  Expected to be done, got", c)
				return
			} else {
				handleMsg(msg[0:msgptr])
				state = EXPECT_START
			}
		}
		if msgptr < MSG_MAX_LEN {
			msg[msgptr] = c
			msgptr++
		} // Else silently discard the rest of the message.  *shrug*
		// This should not happen.
	}
}

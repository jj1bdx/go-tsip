package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"os"
	"sort"
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

type GetSatelliteTrackingStatusCmd struct {
	SatelliteNumber uint8
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

func RadToDeg32(rad float32) float32 { return rad * 180.0 / math.Pi }

func RadToDeg64(rad float64) float64 { return rad * 180.0 / math.Pi }

func Round32(x float32) float32 { return float32(math.Floor(float64(x) + 0.5)) }

func RoundToInt32(x float32) int { return int(Round32(x)) }

func (c *GetSoftwareVersionCmd) PacketID() []byte { return []byte{0x1f} }

func (c *GetSignalLevelCmd) PacketID() []byte { return []byte{0x27} }

func (c *GetSatelliteTrackingStatusCmd) PacketID() []byte { return []byte{0x3c} }

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

type SatelliteTrackingStatusPacket struct {
	PRNnumber            uint8
	SlotAndChannelNumber uint8
	Acquisition          uint8
	Ephemeris            uint8
	SignalLevel          float32
	LastMeasurementTime  float32
	Elevation            float32
	Azimuth              float32
	OldMeasurement       uint8
	IntegerMsec          uint8
	BadData              uint8
	DataCollection       uint8
}

func (p *SecondaryTimingPacket) Handle() {
	fmt.Printf("Secondary packet:  RCV %d, DIS %d, SUR %d PPS-OFFSET: %f CriticalAlarm: %x MinorAlarm: %x DecodeStatus: %x Temp: %f Lat: %f Long: %f Alt: %f \n",
		p.ReceiverMode, p.DiscipliningMode, p.SelfSurveyProgress, p.PPSOffset, p.CriticalAlarms, p.MinorAlarms, p.GPSDecodeStatus, p.Temperature, RadToDeg64(p.Latitude), RadToDeg64(p.Longitude), p.Altitude)
}

func (p *PrimaryTimingPacket) Handle() {
	fmt.Printf("Primary Timing Packet:  %04d/%02d/%02d %02d:%02d:%02d  (GPS offset %d)\n", p.Year, p.Month, p.DayOfMonth, p.Hours, p.Minutes, p.Seconds, p.UTCOffset)
}

func (p *SoftwareVersionPacket) Handle() {
	fmt.Printf("Software Version Response:  App: %d.%d %04d/%02d/%02d  GPS: %d.%d %04d/%02d/%02d\n",
		p.AppMajor, p.AppMinor, int(p.AppYearFrom2000)+2000, p.AppMonth, p.AppDay,
		p.GPSMajor, p.GPSMinor, int(p.GPSYearFrom2000)+2000, p.GPSMonth, p.GPSDay)
}

func (p *PPSCharacteristicsPacket) Handle() {
	fmt.Printf("PPS Characteristics packet: Output-enable %d, Polarity %d, PPS Offset: %f, Bias Threshold: %f\n",
		p.PPSOutputEnable, p.PPSPolarity, p.PPSOffset, p.BiasThreshold)
}

func (p *SatelliteTrackingStatusPacket) Handle() {
	signal := int(p.SignalLevel)
	if signal > 0 {
		fmt.Printf("Satellite Tracking Status:  PRN: %d, Signal: %d, Elev: %d, Azi: %d\n",
			p.PRNnumber, signal, RoundToInt32(RadToDeg32(p.Elevation)), RoundToInt32(RadToDeg32(p.Azimuth)))
	}
}

var actions []Action

func init() {
	// HUMAN:  The parser requires that you list these in descending
	// order of MatchSequence length.
	actions = []Action{
		{[]byte{0x8f, 0xab}, &PrimaryTimingPacket{}},
		{[]byte{0x8f, 0xac}, &SecondaryTimingPacket{}},
		{[]byte{0x8f, 0x4a}, &PPSCharacteristicsPacket{}},
		{[]byte{0x5c}, &SatelliteTrackingStatusPacket{}},
		{[]byte{0x45}, &SoftwareVersionPacket{}},
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

type NumberAndLevelInt struct {
	PRNNumber      uint8
	SignalLevelInt int
}

func handleSatelliteSignalReport(msg []byte) {
	var p NumberAndLevel
	var l []NumberAndLevelInt

	count := int(msg[1])
	r := bytes.NewReader(msg[2:])
	for i := 0; i < count; i++ {
		binary.Read(r, binary.BigEndian, &p)
		level := int(p.SignalLevel)
		if level > 0 {
			l = append(l, NumberAndLevelInt{p.PRNNumber, level})
		}
	}
	sort.Slice(l, func(i, j int) bool {
		return l[i].PRNNumber < l[j].PRNNumber
	})
	fmt.Printf("Satellite Signal Report PRN/Signal: ")
	for _, q := range l {
		fmt.Printf("%d/%d ", q.PRNNumber, q.SignalLevelInt)
	}
	fmt.Printf("\n")
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
		for true {
			time.Sleep(30 * time.Second)
			fmt.Println("Sending GetSatelliteTrackingStatusCmd")
			sendCmd(&GetSatelliteTrackingStatusCmd{0x00})
		}
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

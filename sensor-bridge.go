package main

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"net"
	"time"

	"github.com/brutella/hc"
	"github.com/brutella/hc/accessory"
	"github.com/brutella/hc/characteristic"
	"github.com/brutella/hc/service"
)

type MeasurementData struct {
	Temperature float32 `json:"temperature"`
	Humidity    float32 `json:"humidity"`
	Pressure    float32 `json:"pressure"`
}

type Measurement struct {
	SensorID        string          `json:"sensor_id"`
	SensorTime      int64           `json:"sensor_time"`
	MeasurementID   string          `json:"measurement_id"`
	MeasurementData MeasurementData `json:"measurement_data"`
}

// TODO Needs a mutex
var latestMeasurements map[string]Measurement = map[string]Measurement{}

func process(pc net.PacketConn, address net.Addr, payload []byte) error {
	var measurement Measurement
	if err := json.Unmarshal(payload, &measurement); err != nil {
		return err
	}

	latestMeasurements[measurement.SensorID] = measurement
	log.Printf("%s: Temperature <%f> Humidity <%f>\n", measurement.SensorID,
		measurement.MeasurementData.Temperature, measurement.MeasurementData.Humidity)

	return nil
}

func receiver() {
	pc, err := net.ListenPacket("udp", ":3232")
	if err != nil {
		log.Fatal(err)
	}

	defer pc.Close()

	for {
		buf := make([]byte, 1024)
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			continue
		}

		if err := process(pc, addr, buf[:n]); err != nil {
			log.Println("Failed to process packet: ", err)
		}
	}
}

func createSensor(config SensorConfig, id uint64) (*accessory.Accessory, error) {
	info := accessory.Info{
		Name:         config.Name,
		Manufacturer: "Stefan",
		Model:        config.Model,
		SerialNumber: config.Serial,
		ID:           id,
	}

	ac := accessory.New(info, accessory.TypeSensor)

	tempSensor := service.NewTemperatureSensor()

	tempStatusActive := characteristic.NewStatusActive()
	tempSensor.AddCharacteristic(tempStatusActive.Characteristic)

	tempStatusFault := characteristic.NewStatusFault()
	tempSensor.AddCharacteristic(tempStatusFault.Characteristic)

	var fetchTemperature = func(serial string) interface{} {
		log.Printf("fetchTemperature for %s", serial)
		tempStatusFault.UpdateValue(characteristic.StatusFaultNoFault)
		if measurement, ok := latestMeasurements[serial]; ok {
			tempStatusActive.UpdateValue(true)
			return measurement.MeasurementData.Temperature
		}
		tempStatusActive.UpdateValue(false)
		return 0.0
	}

	tempSensor.CurrentTemperature.OnValueGet(func() interface{} {
		log.Println("tempSensor.CurrentTemperature.OnValueGet")
		return fetchTemperature(config.Serial)
	})

	tempIntervalTicker := time.NewTicker(time.Second * 60)
	tempIntervalTimerChan := make(chan bool)

	go func() {
		for {
			select {
			case <-tempIntervalTimerChan:
				return
			case <-tempIntervalTicker.C:
				tempSensor.CurrentTemperature.UpdateValue(fetchTemperature(config.Serial))
			}
		}
	}()

	ac.AddService(tempSensor.Service)

	return ac, nil
}

type SensorConfig struct {
	Serial string `json:"serial"`
	Name   string `json:"name"`
	Model  string `json:"model"`
}

type BridgeConfig struct {
	Name         string         `json:"name"`
	Manufacturer string         `json:"manufacturer"`
	Model        string         `json:"model"`
	Pin          string         `json:"pin"`
	Sensors      []SensorConfig `json:"sensors"`
	Address      string         `json:"address"`
}

type ReceiverConfig struct {
	Port int `json:"port"`
}

type Config struct {
	Receiver ReceiverConfig `json:"receiver"`
	Bridge   BridgeConfig   `json:"bridge"`
}

func createBridge(config BridgeConfig) (*accessory.Bridge, error) {
	bridgeInfo := accessory.Info{
		Name:         config.Name,
		Manufacturer: config.Manufacturer,
		Model:        config.Model,
		ID:           1,
	}

	return accessory.NewBridge(bridgeInfo), nil
}

func main() {
	log.Println("[*] Starting sensor-hub")
	encodedConfig, err := ioutil.ReadFile("sensor-bridge.json")
	if err != nil {
		log.Fatal("Could not load config file: ", err)
	}

	var config Config
	if err := json.Unmarshal(encodedConfig, &config); err != nil {
		log.Fatal("Could not parse config file: ", err)
	}

	// Create the bridge and sensors

	bridge, err := createBridge(config.Bridge)
	if err != nil {
		log.Fatal("Could not create bridge: ", err)
	}

	var sensors []*accessory.Accessory
	for i, sensorConfig := range config.Bridge.Sensors {
		sensor, err := createSensor(sensorConfig, 2+uint64(i))
		if err != nil {
			log.Fatalf("Could not create sensor <%s>: %v", sensorConfig.Serial, err)
		}
		sensors = append(sensors, sensor)
	}

	// Start it

	hcConfig := hc.Config{
		Pin:         config.Bridge.Pin,
		StoragePath: "data",
		IP:          config.Bridge.Address,
	}

	transport, err := hc.NewIPTransport(hcConfig, bridge.Accessory, sensors...)
	if err != nil {
		log.Fatal("Could not create ip transport: ", err)
	}

	// On termination we stop all timers and then the transport

	hc.OnTermination(func() {
		// TODO Stop all Accessory timers ...
		<-transport.Stop()
	})

	transport.Start()

	log.Println("[*] Done")
}

package main

// DashboardData holds live sensor readings from a Netatmo module.
// All measurement fields are pointers so absent values (wrong module type) are nil.
type DashboardData struct {
	TimeUTC      int64    `json:"time_utc"`
	Temperature  *float64 `json:"Temperature,omitempty"`
	Humidity     *float64 `json:"Humidity,omitempty"`
	CO2          *int     `json:"CO2,omitempty"`
	Pressure     *float64 `json:"Pressure,omitempty"`
	Noise        *int     `json:"Noise,omitempty"`
	WindStrength *int     `json:"WindStrength,omitempty"`
	WindAngle    *int     `json:"WindAngle,omitempty"`
	GustStrength *int     `json:"GustStrength,omitempty"`
	GustAngle    *int     `json:"GustAngle,omitempty"`
	Rain         *float64 `json:"Rain,omitempty"`
	SumRain1     *float64 `json:"sum_rain_1,omitempty"`
	SumRain24    *float64 `json:"sum_rain_24,omitempty"`
}

// Module is an additional sensor module attached to the base station.
type Module struct {
	Type          string        `json:"type"`
	ModuleName    string        `json:"module_name"`
	DashboardData DashboardData `json:"dashboard_data"`
}

// Device is a Netatmo base station (NAMain) together with its attached modules.
type Device struct {
	StationName   string        `json:"station_name"`
	Type          string        `json:"type"`
	ModuleName    string        `json:"module_name"`
	DashboardData DashboardData `json:"dashboard_data"`
	Modules       []Module      `json:"modules"`
}

// StationsDataResponse is the top-level envelope returned by /api/getstationsdata.
type StationsDataResponse struct {
	Body struct {
		Devices []Device `json:"devices"`
	} `json:"body"`
}

// serviceSpec describes one service offered by a module asset.
type serviceSpec struct {
	definition string
	subPath    string
	// unit is a QUDT IRI wherever the framework knows one, so a consumer asking
	// for °F gets a converted reading rather than a refusal. Where it does not —
	// ppm, km/h, mm — the plain symbol stays: an invented IRI would resolve to
	// nothing and read as a promise the conversion cannot keep.
	unit string
	// quantityKind is what the service is discovered by. A consumer asks for a
	// temperature, not for Celsius; leaving this blank leaves the service
	// findable only by a consumer naming the same unit string.
	quantityKind string
	description  string
}

// moduleInfo maps a Netatmo module type to a stable mbaigo asset name and its services.
// When locationFromModuleName is true, the module's user-given name (e.g. "Bathroom")
// is published as FunctionalLocation instead of the station name, allowing consumers
// to discover it by room rather than by weather station.
type moduleInfo struct {
	assetName              string
	locationFromModuleName bool
	services               []serviceSpec
}

// moduleTypeMap translates the Netatmo type field to mbaigo asset name and service list.
var moduleTypeMap = map[string]moduleInfo{
	"NAMain": {
		assetName: "IndoorModule",
		services: []serviceSpec{
			{"temperature", "temperature", "<http://qudt.org/vocab/unit/DEG_C>", "<http://qudt.org/vocab/quantitykind/ThermodynamicTemperature>", "indoor temperature (GET)"},
			{"humidity", "humidity", "<http://qudt.org/vocab/unit/PERCENT>", "<http://qudt.org/vocab/quantitykind/RelativeHumidity>", "indoor relative humidity (GET)"},
			{"co2", "co2", "ppm", "", "indoor CO2 concentration (GET)"},
			{"pressure", "pressure", "<http://qudt.org/vocab/unit/MilliBAR>", "<http://qudt.org/vocab/quantitykind/Pressure>", "atmospheric pressure (GET)"},
			{"noise", "noise", "<http://qudt.org/vocab/unit/DeciB>", "<http://qudt.org/vocab/quantitykind/SoundPressureLevel>", "indoor noise level (GET)"},
		},
	},
	"NAModule1": {
		assetName: "OutdoorModule",
		services: []serviceSpec{
			{"temperature", "temperature", "<http://qudt.org/vocab/unit/DEG_C>", "<http://qudt.org/vocab/quantitykind/ThermodynamicTemperature>", "outdoor temperature (GET)"},
			{"humidity", "humidity", "<http://qudt.org/vocab/unit/PERCENT>", "<http://qudt.org/vocab/quantitykind/RelativeHumidity>", "outdoor relative humidity (GET)"},
		},
	},
	"NAModule2": {
		assetName: "WindModule",
		services: []serviceSpec{
			{"wind_speed", "wind_speed", "km/h", "", "wind speed (GET)"},
			{"wind_angle", "wind_angle", "<http://qudt.org/vocab/unit/DEG>", "<http://qudt.org/vocab/quantitykind/Angle>", "wind direction in degrees (GET)"},
			{"gust_speed", "gust_speed", "km/h", "", "gust speed (GET)"},
			{"gust_angle", "gust_angle", "<http://qudt.org/vocab/unit/DEG>", "<http://qudt.org/vocab/quantitykind/Angle>", "gust direction in degrees (GET)"},
		},
	},
	"NAModule3": {
		assetName: "RainModule",
		services: []serviceSpec{
			{"rain", "rain", "mm/h", "", "rain accumulation in last hour (GET)"},
			{"rain_24h", "rain_24h", "mm", "", "rain accumulation in last 24 hours (GET)"},
		},
	},
	"NAModule4": {
		assetName:              "IndoorModule2",
		locationFromModuleName: true, // publish ModuleName (e.g. "Bathroom") as FunctionalLocation
		services: []serviceSpec{
			{"temperature", "temperature", "<http://qudt.org/vocab/unit/DEG_C>", "<http://qudt.org/vocab/quantitykind/ThermodynamicTemperature>", "indoor temperature, secondary module (GET)"},
			{"humidity", "humidity", "<http://qudt.org/vocab/unit/PERCENT>", "<http://qudt.org/vocab/quantitykind/RelativeHumidity>", "indoor relative humidity, secondary module (GET)"},
			{"co2", "co2", "ppm", "", "indoor CO2 concentration, secondary module (GET)"},
		},
	},
}

// extractMeasurements pulls the relevant float64 readings from a DashboardData
// according to the Netatmo module type and returns them keyed by service subpath.
func extractMeasurements(moduleType string, dd DashboardData) map[string]float64 {
	m := make(map[string]float64)
	switch moduleType {
	case "NAMain":
		if dd.Temperature != nil {
			m["temperature"] = *dd.Temperature
		}
		if dd.Humidity != nil {
			m["humidity"] = *dd.Humidity
		}
		if dd.CO2 != nil {
			m["co2"] = float64(*dd.CO2)
		}
		if dd.Pressure != nil {
			m["pressure"] = *dd.Pressure
		}
		if dd.Noise != nil {
			m["noise"] = float64(*dd.Noise)
		}
	case "NAModule1":
		if dd.Temperature != nil {
			m["temperature"] = *dd.Temperature
		}
		if dd.Humidity != nil {
			m["humidity"] = *dd.Humidity
		}
	case "NAModule2":
		if dd.WindStrength != nil {
			m["wind_speed"] = float64(*dd.WindStrength)
		}
		if dd.WindAngle != nil {
			m["wind_angle"] = float64(*dd.WindAngle)
		}
		if dd.GustStrength != nil {
			m["gust_speed"] = float64(*dd.GustStrength)
		}
		if dd.GustAngle != nil {
			m["gust_angle"] = float64(*dd.GustAngle)
		}
	case "NAModule3":
		if dd.SumRain1 != nil {
			m["rain"] = *dd.SumRain1
		}
		if dd.SumRain24 != nil {
			m["rain_24h"] = *dd.SumRain24
		}
	case "NAModule4":
		if dd.Temperature != nil {
			m["temperature"] = *dd.Temperature
		}
		if dd.Humidity != nil {
			m["humidity"] = *dd.Humidity
		}
		if dd.CO2 != nil {
			m["co2"] = float64(*dd.CO2)
		}
	}
	return m
}

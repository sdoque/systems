// mAF — the mbaigo Arrowhead Framework library in SysML v2.
//
// Encodes the core vocabulary of an Arrowhead local cloud so that concrete
// models (emitted by the modeler) can specialise from typed abstractions
// instead of re-inventing Arrowhead semantics inline. mAF is the SysML v2
// sibling of the AFO OWL ontology: the same concepts seen from a structural
// and behavioural angle, rather than a reasoning angle. Use mAF for
// composition, ports, connections, and behaviour; use AFO for class
// reasoning, property chains, and semantic queries.
//
// Intended use — concrete types specialise from mAF abstractions:
//     part def 'thermostatSystem'                 :> ArrowheadSystem    { ... }
//     part def 'serviceregistrarSystem'           :> ServiceRegistrar   { ... }
//     part def 'thermostat_controller_1UnitAsset' :> UnitAsset          { ... }
//     part def 'alphaCloudDef'                    :> LocalCloud         { ... }

package mAF2 {
	private import ScalarValues::String;
	private import ScalarValues::Integer;
    // ── Abstract action definitions ──────────────────────────────────────────
    // The three elementary actions a unit asset can perform: read a consumed
    // service (GetState), compute a control decision (Compute), and write
    // to a consumed service (SetState). Behaviour defs emitted by the
    // modeler chain these actions with 'first … then …' pairs.
    abstract action def GetState;
    abstract action def SetState;
    abstract action def Compute;

    // ── Host ────────────────────────────────────────────────────────────────
    // A physical or virtual machine on which one or more systems run.
    part def Host {
        attribute name : String;
        attribute ipAddress : String[*];
    }

    // ── Unit Asset ──────────────────────────────────────────────────────────
    // The functional component inside a system — business logic, sensor,
    // actuator. Every concrete unit asset type specialises this.
    abstract part def UnitAsset {
        attribute mission : String;
    }

    // ── System hierarchy ────────────────────────────────────────────────────
    abstract part def ArrowheadSystem {
        attribute name : String;
        attribute host : String;
    }

    // Core systems: Arrowhead infrastructure that mediates a local cloud.
    // Concrete core systems specialise these so their role is queryable
    // structurally (e.g. "find all ServiceRegistrars in this cloud").
    abstract part def CoreSystem :> ArrowheadSystem;
    abstract part def ServiceRegistrar     :> CoreSystem;
    abstract part def Orchestrator         :> CoreSystem;
    abstract part def CertificateAuthority :> CoreSystem;

    // ── Local Cloud ─────────────────────────────────────────────────────────
    // A set of Arrowhead systems deployed under a single administrative
    // domain. Concrete cloud models specialise this definition with their
    // specific host and system part usages.
    abstract part def LocalCloud {
        attribute name : String;
    }
}

package 'localCloud' {
	private import ScalarValues::String;
	private import ScalarValues::Integer;
    private import mAF2::*;

    // ── Port Definitions ─────────────────────────────────────────────────────
    port def 'Inside_temperature';
    port def 'Inside_humidity';
    port def 'Barometer';
    port def 'Wind_speed';
    port def 'Wind_angle';
    port def 'Rain_rate';
    port def 'Rain_24h';
    port def 'Temperature';
    port def 'Humidity';
    port def 'Uv';
    port def 'Solar_radiation';
    port def 'Register';
    port def 'Query';
    port def 'Unregister';
    port def 'Status';
    port def 'Setpoint';
    port def 'Thermalerror';
    port def 'Jitter';
    port def 'Rotation';
    port def 'Squest';
    port def 'Cloudgraph';
    port def 'LocalOntologies';
    port def 'Cloudmodel';

    // ── Block Definitions (BDD) ──────────────────────────────────────────────
    part def 'weathermanSystem' :> ArrowheadSystem {
        attribute redefines name : String = "weatherman";
        attribute redefines host : String;
        attribute httpPort : Integer;
        part 'ConsoleModule' : 'weatherman_ConsoleModuleUnitAsset';
        part 'ISSModule' : 'weatherman_ISSModuleUnitAsset';
        part 'SolarModule' : 'weatherman_SolarModuleUnitAsset';
    }

    part def 'weatherman_ConsoleModuleUnitAsset' :> UnitAsset {
        attribute redefines mission = "provide_weather_data";
        out port 'barometer' : 'Barometer';  // provided service
        out port 'inside_temperature' : 'Inside_temperature';  // provided service
        out port 'inside_humidity' : 'Inside_humidity';  // provided service
    }

    part def 'weatherman_ISSModuleUnitAsset' :> UnitAsset {
        attribute redefines mission = "provide_weather_data";
        out port 'wind_angle' : 'Wind_angle';  // provided service
        out port 'rain_rate' : 'Rain_rate';  // provided service
        out port 'rain_24h' : 'Rain_24h';  // provided service
        out port 'temperature' : 'Temperature';  // provided service
        out port 'humidity' : 'Humidity';  // provided service
        out port 'wind_speed' : 'Wind_speed';  // provided service
    }

    part def 'weatherman_SolarModuleUnitAsset' :> UnitAsset {
        attribute redefines mission = "provide_weather_data";
        out port 'uv' : 'Uv';  // provided service
        out port 'solar_radiation' : 'Solar_radiation';  // provided service
    }

    part def 'serviceregistrarSystem' :> ServiceRegistrar {
        attribute redefines name = "serviceregistrar";
        attribute redefines host ;
        attribute httpPort : Integer;
        part 'registry' : 'serviceregistrar_registryUnitAsset';
    }

    part def 'serviceregistrar_registryUnitAsset' :> UnitAsset {
        out port 'query' : 'Query';  // provided service
        out port 'unregister' : 'Unregister';  // provided service
        out port 'status' : 'Status';  // provided service
        out port 'register' : 'Register';  // provided service
    }

    part def 'thermostatSystem' :> ArrowheadSystem {
        attribute redefines name : String = "thermostat";
        attribute redefines host : String;
        attribute httpPort : Integer;
        part 'controller_1' : 'thermostat_controller_1UnitAsset';
    }

    part def 'thermostat_controller_1UnitAsset' :> UnitAsset {
        attribute redefines mission : String = "control_heater";
        out port 'thermalerror' : 'Thermalerror';  // provided service
        out port 'jitter' : 'Jitter';  // provided service
        out port 'setpoint' : 'Setpoint';  // provided service
        in port 'temperature' : 'Temperature';  // consumed service
        in port 'rotation' : 'Rotation';  // consumed service
        perform action behave : 'thermostat_controller_1Behavior';
    }

    part def 'orchestratorSystem' :> Orchestrator {
        attribute redefines name : String = "orchestrator";
        attribute  redefines host : String;
        attribute httpPort : Integer;
        part 'orchestration' : 'orchestrator_orchestrationUnitAsset';
    }

    part def 'orchestrator_orchestrationUnitAsset' :> UnitAsset {
        out port 'squest' : 'Squest';  // provided service
    }

    part def 'kgrapherSystem' :> ArrowheadSystem {
        attribute redefines name : String = "kgrapher";
        attribute redefines host : String;
        attribute  httpPort : Integer;
        part 'assembler' : 'kgrapher_assemblerUnitAsset';
    }

    part def 'kgrapher_assemblerUnitAsset' :> UnitAsset {
        attribute redefines mission : String = "handle_triplestore";
        out port  'cloudgraph' : 'Cloudgraph';  // provided service
        out port  'localOntologies' : 'LocalOntologies';  // provided service
    }

    part def 'ds18b20System' :> ArrowheadSystem {
        attribute redefines name : String = "ds18b20";
        attribute redefines host : String;
        attribute  httpPort : Integer;
        part '28-00000f030344' : 'ds18b20_28_00000f030344UnitAsset';
    }

    part def 'ds18b20_28_00000f030344UnitAsset' :> UnitAsset {
        attribute redefines mission : String = "measure_temperature";
        out port 'temperature' : 'Temperature';  // provided service
    }

    part def 'parallaxSystem' :> ArrowheadSystem {
        attribute redefines name : String = "parallax";
        attribute redefines host : String;
        attribute  httpPort : Integer;
        part 'Servo_1' : 'parallax_Servo_1UnitAsset';
    }

    part def 'parallax_Servo_1UnitAsset' :> UnitAsset {
        attribute redefines mission : String = "actuate_servo";
        out port 'rotation' : 'Rotation';  // provided service
    }

    part def 'modelerSystem' :> ArrowheadSystem {
        attribute redefines name : String = "modeler";
        attribute redefines host : String;
        attribute httpPort : Integer;
        part 'assembler' : 'modeler_assemblerUnitAsset';
    }

    part def 'modeler_assemblerUnitAsset' :> UnitAsset {
        attribute redefines mission : String = "handle_sysml";
        out port 'cloudmodel' : 'Cloudmodel';  // provided service
    }

    part def 'localCloudDef' :> LocalCloud {
        part canbus : Host;
        part ds18b20 : 'ds18b20System';
        part kgrapher : 'kgrapherSystem';
        part modeler : 'modelerSystem';
        part orchestrator : 'orchestratorSystem';
        part parallax : 'parallaxSystem';
        part serviceregistrar : 'serviceregistrarSystem';
        part thermostat : 'thermostatSystem';
        part weatherman : 'weathermanSystem';
    }

    // ── Behaviour Definitions ────────────────────────────────────────────────
    action def 'thermostat_controller_1Behavior' {
        action 'get_temperature' : GetState;
        action compute : Compute;
        action 'set_rotation' : SetState;

        first 'get_temperature' then compute;
        first compute then 'set_rotation';
    }

    // ── Internal Block Diagram (IBD) ─────────────────────────────────────────
    part 'localCloud' : 'localCloudDef' {
        attribute redefines name : String = "localCloud";

        part redefines canbus {
            attribute redefines name : String = "canbus";
            attribute  redefinesipAddress : String = "192.168.1.10";
        }

        part redefines ds18b20 {
            attribute redefines host : String = "canbus";
            attribute  redefineshttpPort : Integer = 20150;
            // provides: http://192.168.1.10:20150/ds18b20/28-00000f030344/temperature
        }

        part redefines kgrapher {
            attribute redefines host : String = "canbus";
            attribute redefines httpPort : Integer = 20105;
            // provides: http://192.168.1.10:20105/kgrapher/assembler/cloudgraph
            // provides: http://192.168.1.10:20105/kgrapher/assembler/localontologies
        }

        part redefines modeler {
            attribute redefines host : String = "canbus";
            attribute redefines  httpPort : Integer = 20106;
            // provides: http://192.168.1.10:20106/modeler/assembler/cloudmodel
        }

        part redefines orchestrator {
            attribute redefines host : String = "canbus";
            attribute redefines httpPort : Integer = 20103;
            // provides: http://192.168.1.10:20103/orchestrator/orchestration/squest
        }

        part redefines parallax {
            attribute redefines host : String = "canbus";
            attribute redefines httpPort : Integer = 20151;
            // provides: http://192.168.1.10:20151/parallax/Servo_1/rotation
        }

        part redefines serviceregistrar {
            attribute redefines host : String = "canbus";
            attribute redefines httpPort : Integer = 20102;
            // provides: http://192.168.1.10:20102/serviceregistrar/registry/query
            // provides: http://192.168.1.10:20102/serviceregistrar/registry/unregister
            // provides: http://192.168.1.10:20102/serviceregistrar/registry/status
            // provides: http://192.168.1.10:20102/serviceregistrar/registry/register
        }

        part redefines thermostat {
            attribute redefines host : String = "canbus";
            attribute redefines httpPort : Integer = 20152;
            // provides: http://192.168.1.10:20152/thermostat/controller_1/setpoint
            // provides: http://192.168.1.10:20152/thermostat/controller_1/thermalerror
            // provides: http://192.168.1.10:20152/thermostat/controller_1/jitter
        }

        part redefines weatherman {
            attribute redefines host : String = "canbus";
            attribute redefines httpPort : Integer = 20184;
            // provides: http://192.168.1.10:20184/weatherman/ConsoleModule/inside_humidity
            // provides: http://192.168.1.10:20184/weatherman/ConsoleModule/barometer
            // provides: http://192.168.1.10:20184/weatherman/ConsoleModule/inside_temperature
            // provides: http://192.168.1.10:20184/weatherman/ISSModule/wind_speed
            // provides: http://192.168.1.10:20184/weatherman/ISSModule/wind_angle
            // provides: http://192.168.1.10:20184/weatherman/ISSModule/rain_rate
            // provides: http://192.168.1.10:20184/weatherman/ISSModule/rain_24h
            // provides: http://192.168.1.10:20184/weatherman/ISSModule/temperature
            // provides: http://192.168.1.10:20184/weatherman/ISSModule/humidity
            // provides: http://192.168.1.10:20184/weatherman/SolarModule/uv
            // provides: http://192.168.1.10:20184/weatherman/SolarModule/solar_radiation
        }

        // ── Connections ──────────────────────────────────────────────────────
        connect thermostat.controller_1.temperature to ds18b20.'28-00000f030344'.temperature;
        connect thermostat.controller_1.rotation to parallax.Servo_1.rotation;
    }
}

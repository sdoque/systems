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

package mAF {

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

    import mAF::*;

    // ── Port Definitions ─────────────────────────────────────────────────────
    port def 'inside_temperature';
    port def 'inside_humidity';
    port def 'barometer';
    port def 'wind_speed';
    port def 'wind_angle';
    port def 'rain_rate';
    port def 'rain_24h';
    port def 'temperature';
    port def 'humidity';
    port def 'uv';
    port def 'solar_radiation';
    port def 'register';
    port def 'query';
    port def 'unregister';
    port def 'status';
    port def 'setpoint';
    port def 'thermalerror';
    port def 'jitter';
    port def 'rotation';
    port def 'squest';
    port def 'cloudgraph';
    port def 'localOntologies';
    port def 'cloudmodel';

    // ── Block Definitions (BDD) ──────────────────────────────────────────────
    part def 'weathermanSystem' :> ArrowheadSystem {
        attribute name : String = "weatherman";
        attribute host : String;
        attribute httpPort : Integer;
        part 'ConsoleModule' : 'weatherman_ConsoleModuleUnitAsset';
        part 'ISSModule' : 'weatherman_ISSModuleUnitAsset';
        part 'SolarModule' : 'weatherman_SolarModuleUnitAsset';
    }

    part def 'weatherman_ConsoleModuleUnitAsset' :> UnitAsset {
        attribute mission : String = "provide_weather_data";
        out port 'barometer' : 'barometer';  // provided service
        out port 'inside_temperature' : 'inside_temperature';  // provided service
        out port 'inside_humidity' : 'inside_humidity';  // provided service
    }

    part def 'weatherman_ISSModuleUnitAsset' :> UnitAsset {
        attribute mission : String = "provide_weather_data";
        out port 'wind_angle' : 'wind_angle';  // provided service
        out port 'rain_rate' : 'rain_rate';  // provided service
        out port 'rain_24h' : 'rain_24h';  // provided service
        out port 'temperature' : 'temperature';  // provided service
        out port 'humidity' : 'humidity';  // provided service
        out port 'wind_speed' : 'wind_speed';  // provided service
    }

    part def 'weatherman_SolarModuleUnitAsset' :> UnitAsset {
        attribute mission : String = "provide_weather_data";
        out port 'uv' : 'uv';  // provided service
        out port 'solar_radiation' : 'solar_radiation';  // provided service
    }

    part def 'serviceregistrarSystem' :> ServiceRegistrar {
        attribute name : String = "serviceregistrar";
        attribute host : String;
        attribute httpPort : Integer;
        part 'registry' : 'serviceregistrar_registryUnitAsset';
    }

    part def 'serviceregistrar_registryUnitAsset' :> UnitAsset {
        out port 'query' : 'query';  // provided service
        out port 'unregister' : 'unregister';  // provided service
        out port 'status' : 'status';  // provided service
        out port 'register' : 'register';  // provided service
    }

    part def 'thermostatSystem' :> ArrowheadSystem {
        attribute name : String = "thermostat";
        attribute host : String;
        attribute httpPort : Integer;
        part 'controller_1' : 'thermostat_controller_1UnitAsset';
    }

    part def 'thermostat_controller_1UnitAsset' :> UnitAsset {
        attribute mission : String = "control_heater";
        out port 'thermalerror' : 'thermalerror';  // provided service
        out port 'jitter' : 'jitter';  // provided service
        out port 'setpoint' : 'setpoint';  // provided service
        in port 'temperature' : 'temperature';  // consumed service
        in port 'rotation' : 'rotation';  // consumed service
        perform action behave : 'thermostat_controller_1Behavior';
    }

    part def 'orchestratorSystem' :> Orchestrator {
        attribute name : String = "orchestrator";
        attribute host : String;
        attribute httpPort : Integer;
        part 'orchestration' : 'orchestrator_orchestrationUnitAsset';
    }

    part def 'orchestrator_orchestrationUnitAsset' :> UnitAsset {
        out port 'squest' : 'squest';  // provided service
    }

    part def 'kgrapherSystem' :> ArrowheadSystem {
        attribute name : String = "kgrapher";
        attribute host : String;
        attribute httpPort : Integer;
        part 'assembler' : 'kgrapher_assemblerUnitAsset';
    }

    part def 'kgrapher_assemblerUnitAsset' :> UnitAsset {
        attribute mission : String = "handle_triplestore";
        out port 'cloudgraph' : 'cloudgraph';  // provided service
        out port 'localOntologies' : 'localOntologies';  // provided service
    }

    part def 'ds18b20System' :> ArrowheadSystem {
        attribute name : String = "ds18b20";
        attribute host : String;
        attribute httpPort : Integer;
        part '28-00000f030344' : 'ds18b20_28_00000f030344UnitAsset';
    }

    part def 'ds18b20_28_00000f030344UnitAsset' :> UnitAsset {
        attribute mission : String = "measure_temperature";
        out port 'temperature' : 'temperature';  // provided service
    }

    part def 'parallaxSystem' :> ArrowheadSystem {
        attribute name : String = "parallax";
        attribute host : String;
        attribute httpPort : Integer;
        part 'Servo_1' : 'parallax_Servo_1UnitAsset';
    }

    part def 'parallax_Servo_1UnitAsset' :> UnitAsset {
        attribute mission : String = "actuate_servo";
        out port 'rotation' : 'rotation';  // provided service
    }

    part def 'modelerSystem' :> ArrowheadSystem {
        attribute name : String = "modeler";
        attribute host : String;
        attribute httpPort : Integer;
        part 'assembler' : 'modeler_assemblerUnitAsset';
    }

    part def 'modeler_assemblerUnitAsset' :> UnitAsset {
        attribute mission : String = "handle_sysml";
        out port 'cloudmodel' : 'cloudmodel';  // provided service
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
        attribute name : String = "localCloud";

        part redefines canbus {
            attribute name : String = "canbus";
            attribute ipAddress : String = "192.168.1.10";
        }

        part redefines ds18b20 {
            attribute host : String = "canbus";
            attribute httpPort : Integer = 20150;
            // provides: http://192.168.1.10:20150/ds18b20/28-00000f030344/temperature
        }

        part redefines kgrapher {
            attribute host : String = "canbus";
            attribute httpPort : Integer = 20105;
            // provides: http://192.168.1.10:20105/kgrapher/assembler/cloudgraph
            // provides: http://192.168.1.10:20105/kgrapher/assembler/localontologies
        }

        part redefines modeler {
            attribute host : String = "canbus";
            attribute httpPort : Integer = 20106;
            // provides: http://192.168.1.10:20106/modeler/assembler/cloudmodel
        }

        part redefines orchestrator {
            attribute host : String = "canbus";
            attribute httpPort : Integer = 20103;
            // provides: http://192.168.1.10:20103/orchestrator/orchestration/squest
        }

        part redefines parallax {
            attribute host : String = "canbus";
            attribute httpPort : Integer = 20151;
            // provides: http://192.168.1.10:20151/parallax/Servo_1/rotation
        }

        part redefines serviceregistrar {
            attribute host : String = "canbus";
            attribute httpPort : Integer = 20102;
            // provides: http://192.168.1.10:20102/serviceregistrar/registry/query
            // provides: http://192.168.1.10:20102/serviceregistrar/registry/unregister
            // provides: http://192.168.1.10:20102/serviceregistrar/registry/status
            // provides: http://192.168.1.10:20102/serviceregistrar/registry/register
        }

        part redefines thermostat {
            attribute host : String = "canbus";
            attribute httpPort : Integer = 20152;
            // provides: http://192.168.1.10:20152/thermostat/controller_1/setpoint
            // provides: http://192.168.1.10:20152/thermostat/controller_1/thermalerror
            // provides: http://192.168.1.10:20152/thermostat/controller_1/jitter
        }

        part redefines weatherman {
            attribute host : String = "canbus";
            attribute httpPort : Integer = 20184;
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

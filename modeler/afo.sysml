/**
 * Arrowhead Framework SysML v2 Domain Library
 *
 * This library package is a SysML v2 Domain Specific Language (DSL)
 * derived from the Arrowhead Framework Ontology (AFO):
 *   http://www.synecdoque.com/2025/afo/ont/arrowhead
 *
 * It provides the structural vocabulary for describing a local cloud —
 * a system of systems — in terms of Hosts, Systems, Husks, Endpoints,
 * Unit Assets, and the Services they provide and consume.
 *
 * Usage:
 *   import 'afo.sysml' as afo;
 *
 *   package 'myCloud' {
 *       part cloud : afo::LocalCloud { ... }
 *   }
 *
 * AFO source: http://www.synecdoque.com/2025/afo/ont/arrowhead
 * AFO version: 1.0.0
 * Creator: Oskar Wintercorn
 * DSL adaptation: Jan A. van Deventer
 */

library package 'ArrowheadFramework' {

    // ═══════════════════════════════════════════════════════════════════════════
    // Primitive Attribute Definitions
    // These specialise the built-in SysML String/Real/Boolean types and give
    // domain-meaningful names to the scalar values used throughout the DSL.
    // ═══════════════════════════════════════════════════════════════════════════

    /** A service definition name (e.g. "temperature", "setpoint"). */
    attribute def 'ServiceDefinition' :> String;

    /** A Uniform Resource Identifier (e.g. an HTTP endpoint URL). */
    attribute def 'URI' :> String;

    /** An IP address string (IPv4 or IPv6). */
    attribute def 'IPAddress' :> String;

    /**
     * The mission of a unit asset — a short, machine-readable tag that
     * describes its purpose (e.g. "control_heater", "measure_temperature").
     * Used in access-control policy reasoning.
     */
    attribute def 'Mission' :> String;

    /** A communication protocol identifier: "http", "https", or "coap". */
    attribute def 'Protocol' :> String;

    // ═══════════════════════════════════════════════════════════════════════════
    // Port Type Definition
    // A ServicePort models a single service interface.  The un-conjugated form
    // is placed on the *consuming* side; the conjugate (~ServicePort) is placed
    // on the *providing* side, reversing the directional semantics.
    // ═══════════════════════════════════════════════════════════════════════════

    /**
     * ServicePort — the interface type for every Arrowhead service.
     *
     * afo:Service maps to this port definition:
     *   afo:hasServiceDefinition → definition
     *   afo:hasUrl               → url
     *   afo:isSubscribAble       → subscribable
     *   afo:hasForms             → forms
     */
    port def 'ServicePort' {
        attribute definition   : 'ServiceDefinition';
        attribute url          : 'URI'    [0..1];
        attribute subscribable : Boolean           = false;
        attribute forms        : String   [0..*];
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // Location  (afo:Location)
    // ═══════════════════════════════════════════════════════════════════════════

    /**
     * Physical location of a Host or a unit asset.
     * afo:Location
     */
    part def 'Location' {
        attribute building  : String [0..1];
        attribute room      : String [0..1];
        attribute latitude  : Real   [0..1];
        attribute longitude : Real   [0..1];
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // Host  (afo:Host)
    // ═══════════════════════════════════════════════════════════════════════════

    /**
     * A computing device on which one or more Systems run.
     *
     * afo:Host
     *   afo:hasName        → name
     *   afo:hasIPaddress   → ipAddresses  (1..*)
     *   afo:hasLocation    → location     (0..1)
     */
    part def 'Host' {
        attribute name        : String;
        attribute ipAddresses : 'IPAddress' [1..*];
        ref       location    : 'Location'  [0..1];
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // Endpoint  (afo:Endpoint — implicit in the ontology, explicit in the KGraph)
    // Binds a protocol and port number to a Host, forming a concrete network
    // address at which a Husk's servers can be reached.
    // ═══════════════════════════════════════════════════════════════════════════

    /**
     * A protocol+port combination on a Host.
     *
     * afo:Endpoint
     *   afo:usesProtocol → protocol
     *   afo:usesPort     → port
     *   afo:onHost       → host
     */
    part def 'Endpoint' {
        attribute protocol : 'Protocol';
        attribute port     : Integer;
        ref       host     : 'Host';
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // Husk  (afo:Husk)
    // The networking shell of a System: owns the Endpoints through which the
    // System's services are reachable.
    // ═══════════════════════════════════════════════════════════════════════════

    /**
     * The wrapper (shell) of a System that manages its network exposure.
     *
     * afo:Husk
     *   afo:aggregates       → endpoints  (1..*)
     *   afo:runsOnHost (inv) → host
     */
    part def 'Husk' {
        part endpoints : 'Endpoint' [1..*];
        ref  host      : 'Host';
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // Service hierarchy  (afo:Service, afo:ProvidedService, afo:ConsumedService)
    // ═══════════════════════════════════════════════════════════════════════════

    /**
     * Abstract base for all services.
     *
     * afo:Service
     *   afo:hasServiceDefinition  → definition
     *   afo:hasName               → name (= assetName/definition)
     *   afo:hasUrl                → url
     *   afo:isSubscribAble        → subscribable
     *   afo:hasForms              → forms
     *   afo:hasRegistrationPeriod → registrationPeriod
     *   afo:hasCarbonFootprint    → carbonFootprint
     *   afo:hasCost               → cost
     *   afo:hasCostUnit           → costUnit
     */
    abstract part def 'Service' {
        attribute definition         : 'ServiceDefinition';
        attribute subPath            : String;
        attribute url                : 'URI'    [0..1];
        attribute subscribable       : Boolean           = false;
        attribute forms              : String   [0..*];
        attribute registrationPeriod : Integer  [0..1];
        attribute carbonFootprint    : Real     [0..1];  // kg CO₂-eq per call
        attribute cost               : Real     [0..1];
        attribute costUnit           : String   [0..1];
        ref       endpoint           : 'Endpoint' [0..1];
    }

    /**
     * A service that a Unit Asset exposes to the rest of the local cloud.
     * afo:ProvidedService
     */
    part def 'ProvidedService' :> 'Service';

    /**
     * A service that a Unit Asset consumes from another system.
     * Nodes (resolved providers) are recorded as providerUrls.
     *
     * afo:ConsumedService
     *   afo:consumes  → definition  (inherited)
     *   afo:fromUrl   → providerUrls
     */
    part def 'ConsumedService' :> 'Service' {
        attribute providerUrls : 'URI' [0..*];
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // UnitAsset  (afo:UnitAsset)
    // ═══════════════════════════════════════════════════════════════════════════

    /**
     * A unit asset encapsulates the functional capability of a System.
     * It provides one or more services and may consume services from peers.
     *
     * afo:UnitAsset
     *   afo:hasName         → name
     *   afo:hasMission      → mission
     *   afo:providesService → provided  (1..*)
     *   afo:consumesService → consumed  (0..*)
     *   afo:hasLocation     → location  (0..1)
     *
     * Port convention (SysML v2):
     *   out port provided : ~'ServicePort' — the conjugated (providing) side
     *   in  port consumed :  'ServicePort' — the required (consuming) side
     */
    part def 'UnitAsset' {
        attribute name    : String;
        attribute mission : 'Mission' [0..1];

        part services : 'ProvidedService' [1..*];
        part cervices : 'ConsumedService' [0..*];
        ref  location : 'Location'        [0..1];

        out port provided : ~'ServicePort' [1..*];  // provided: conjugated
        in  port consumed :  'ServicePort' [0..*];  // consumed: required
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // System  (afo:System)
    // ═══════════════════════════════════════════════════════════════════════════

    /**
     * An Arrowhead compliant System: a Husk wrapping one or more Unit Assets,
     * running on a Host.
     *
     * afo:System
     *   afo:hasName      → name
     *   afo:hasHusk      → husk       (1..1, functional)
     *   afo:hasUnitAsset → unitAssets (1..*)
     *   afo:runsOnHost   → host       (1..1)
     */
    part def 'System' {
        attribute name : String;

        part husk       : 'Husk'      [1];
        part unitAssets : 'UnitAsset' [1..*];
        ref  host       : 'Host'      [1];
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // ServiceRegistrar  — specialisation of System
    // ═══════════════════════════════════════════════════════════════════════════

    /**
     * A core system that maintains the service registry of a Local Cloud.
     * Every Local Cloud must have at least one ServiceRegistrar.
     */
    part def 'ServiceRegistrar' :> 'System';

    // ═══════════════════════════════════════════════════════════════════════════
    // LocalCloud  (afo:LocalCloud)
    // ═══════════════════════════════════════════════════════════════════════════

    /**
     * A Local Cloud — the top-level assembly of an Arrowhead deployment.
     * Contains a mandatory Service Registrar, the participating Systems,
     * and the Hosts they run on.
     *
     * afo:LocalCloud
     *   afo:hasServiceRegistrar → serviceRegistrars (1..*)
     *   afo:hasSystem           → systems           (1..*)
     */
    part def 'LocalCloud' {
        attribute name : String;

        part serviceRegistrars : 'ServiceRegistrar' [1..*];
        part systems           : 'System'            [1..*];
        part hosts             : 'Host'              [1..*];
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // ResilientCloud  (afo:ResilientCloud)
    // ═══════════════════════════════════════════════════════════════════════════

    /**
     * A resilient Local Cloud that tolerates the loss of a single Service
     * Registrar by requiring at least two.
     *
     * afo:ResilientCloud :> afo:LocalCloud
     */
    part def 'ResilientCloud' :> 'LocalCloud' {
        part serviceRegistrars : 'ServiceRegistrar' [2..*];  // override: min 2
    }

}

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

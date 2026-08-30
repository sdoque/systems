# Deploying a local cloud

This is the technician's document. [SECURITY.md](SECURITY.md) says what each
control is for; [`shells/`](https://github.com/sdoque/shells) holds the scripts;
this says what to do, in what order, on which machine — and what it looks like
when it has gone wrong.

**Status: written 30 August 2026 against one host, before the second existed.**
The two-host section is the plan; it will be corrected against the first real
two-host run. Read it as such until this line is gone.

---

## What a cloud is made of

Every host runs a **maitreD** (it attests the binaries on that host) and,
after 30 August, a **registrar** (so a system's default — "the registrar on my
own host" — is right everywhere). One host runs the **CA**. One runs the
**orchestrator** and, if the cloud enforces authorization, the **authorizer**.
Everything else is application systems and goes wherever there is room.

Two facts shape every layout below:

- **The maitreD reads `/proc/<pid>/exe`**, which Linux allows only for a
  process of the same user. So a maitreD must run as the user that runs the
  systems it attests.
- **The CA never enrolls.** It is its own root and attests nothing about
  itself, so it needs no maitreD and need not share anyone's user — which is
  what lets it be isolated.

## Three shapes

### One host

Everything on one machine. The cottage and aiko are this.

The recommended form runs **the CA as its own user**. Its private key,
`ca_private_key.pem`, is then unreadable by the user every other system runs
as; nothing that enrolls can read the root it enrolled against. It costs one
extra tmux session and one extra systemd unit, and nothing else changes: the
CA is reached over the network by every system anyway, and loopback is a
network.

```
user ca   : ca
user jan  : maitreD, serviceregistrar, orchestrator, authorizer, <systems...>
```

### Two hosts

```
host A : ca, maitreD, serviceregistrar, orchestrator, authorizer, <systems...>
host B :     maitreD, serviceregistrar,                           <systems...>
```

Host B's files need the CA's address, and B's registrar needs A's registrar as
the seed of the election. That is all. See *A second host* below.

### Three or more, with the CA on its own

```
CA host : ca                       — nothing enrolled runs here, so no maitreD
host A  : maitreD, serviceregistrar, orchestrator, authorizer, <systems...>
host B  : maitreD, serviceregistrar, <systems...>
host C  : maitreD, serviceregistrar, <systems...>
```

The key lives on a machine no application binary runs on. Two network facts
have to hold, and a firewall that honours one and not the other fails in a way
that looks like something else:

- every host reaches the CA **inbound** on its plaintext port (20100) to ask
  for a certificate;
- the CA reaches **out** to every host's maitreD (port 20101) to attest the
  process asking.

If the second is blocked, every system on that host reports *the CA refused to
certify (403): Attestation failed* — which reads as a whitelist problem and is
a firewall.

---

## The workflow

### 1. On the build machine, once

```
make rpi && make whitelist
```

`make rpi` cross-compiles every system into `rpiExec/`. `make whitelist`
hashes them into `rpiExec/ca/whitelist.json`. **The whitelist is the
release**: a binary not hashed in this step will never be issued a
certificate, and a binary rebuilt after it has a different hash and is a
different binary as far as the cloud is concerned.

Two things about the Makefile worth knowing before they cost an afternoon.
Each system's rule depends on its own `.go` files only, so **a change in
`mbaigo` rebuilds nothing** — `rm rpiExec/*/*_rpi64 && make rpi` forces it.
And the whitelist must be regenerated after any rebuild, or the CA will refuse
the binaries you just built.

### 2. On the CA host

Copy `rpiExec/ca/` — the binary, `whitelist.json`, and (after the first run)
`systemconfig.json`. Start it once to generate the configuration, then set, in
the `certification` asset's traits:

```json
"maitreDHosts": ["127.0.0.1", "::1", "10.0.0.33", "10.0.0.34"],
"maitreDPort":  20101
```

**`maitreDHosts` must list every host's IP address**, including the CA's own
if systems run there. This is the step most often missed, and it fails in the
worst way: a maitreD on an unlisted host is refused enrollment, so nothing on
that host can be attested, so nothing on that host starts — and the message
each system prints is about the CA, not about this list. `TODO.md` has the
full account.

### 3. On each host

Copy `rpiExec/` — or run `download_systems.sh` from `shells/`, then copy the
whitelist into `ca/` by hand, since the downloader cannot ship it.

Write `systems.txt`. Order matters at the top and not much after it:

```
maitreD
serviceregistrar
<systems...>
envoy -serve view cloudpicture
```

with `ca` first on the CA host and `authorizer` before the systems on the
host that runs it. A line may carry arguments; `envoy` needs them.

Start once — `./start_systems.sh` — and let every system write its
`systemconfig.json`. Each will refuse to run the first time, saying so; that
is the generation step, not a failure. Then edit, per system, only what the
template could not know:

| edit | on which hosts | why the template cannot know it |
|---|---|---|
| the CA's URL | every host but the CA's | the template writes *this host*; the CA is elsewhere |
| the authorizer's URL | every host, if the cloud enforces | naming one turns enforcement on — a decision, not a default |
| the other registrars | the registrar's own file only | the election needs a seed |

Nothing else. The registrar defaults to this host and is right; the
orchestrator and the standby registrars are learned from the lead once the
system has enrolled, and kept in `coresystems.cache.json` beside the
configuration — see [esr/README.md](esr/README.md), *More than one host*.

Then `./stop_systems.sh && ./start_systems.sh`.

### 4. On the authorizer host

Write `policies.json` beside the binary, starting from
`policies.alphacloud.example.json` or `policies.cottage.example.json`. Without
one the authorizer denies everything and says so at startup — once, in a line
that is easy to scroll past.

### 5. Verify

Open the canvas — `http://127.0.0.1:8190/` on the host running `envoy`, or
through `ssh -L 8190:127.0.0.1:8190 <host>` from your desk. This is the step
the whole document exists for: it turns *did it work* from reading fourteen
terminals into looking at one picture.

- **Every disk green.** Blue means identified but not authorized — a system
  that names no authorizer, or names one it cannot reach. Orange means
  enrolling and stuck there. Red means open: no CA at all.
- **No pulsing ring.** A ring is a system that asked for a service nobody
  provides, or that somebody provides and it is not bound to. Click it; the
  panel says which.
- **Lines where you expect them.** A controller with no line to its sensor is
  a controller with no input, and it looks healthy from every other angle.

Then, if the assessor runs, read its CSV: every finding it makes about the
model — a mobility not declared, a setpoint with no range — is a configuration
you can fix before the cloud is doing anything that matters.

### 6. Make it come back

Install `shells/mbaigo-cloud.service` so the cloud restarts with the host.
The cottage exists because of this step: a cloud that does not come back after
a power cut is, in a Norrbotten winter, a burst pipe.

---

## A second host

The plan, to be corrected against the first real run:

1. Add its IP to `maitreDHosts` on the CA host; restart the CA.
2. Copy `rpiExec/`; `systems.txt` = `maitreD`, `serviceregistrar`, its systems.
3. Start once to generate; set the CA's URL in every file, the authorizer's if
   enforcing; add the first host's registrar to the registrar's file.
4. Start. On the canvas a second host disk appears, its registrar answers
   `/status` as *on standby*, and its systems' lines cross to the first host's
   providers.

Expect the first five minutes to say *not in whitelist* — see below.

---

## When it does not start

**Everything says *the CA refused to certify (403): Attestation failed —
Executable not in whitelist*, for about five minutes, then works.** The
maitreD loads `whitelist.cache.json` at startup and re-syncs from the CA every
five minutes. After any rebuild every hash has changed and the cached list is
the old one. Wait, or restart the maitreD.

**The same, and it never clears.** One of: the whitelist was not regenerated
after the build; the copy on the CA host is older than the binaries; the CA
cannot reach this host's maitreD (firewall, or `maitreDPort` wrong); or the
binary was rebuilt after the whitelist was made — `sha256sum <binary>` and
`grep` it in `ca/whitelist.json` settles which.

**Nothing on a host starts, and the maitreD says the CA refused it.** That
host is not in `maitreDHosts`. Step 2.

**A system is up, green, and bound to nothing.** Click it. *Nothing in this
cloud offers it* is a missing system. *Offered by X, but not bound to it* is
policy — the authorizer refused, or there is no `policies.json` — or a quest
over-specified by a detail that does not match. The orchestrator's log names
the details the quest required.

**Discovery fails with *this quest arrived without a client certificate*.**
The orchestrator's URL in that system's file is `http://`. It must be
`https://`: a consumer reaches the orchestrator only after enrolling, and an
authorized cloud refuses a quest it cannot name. The generated default is
right; a file written before 30 August 2026 is not.

**Discovery fails with *connection refused* on an `https://` orchestrator.**
The cloud has no CA, so nothing serves TLS. Set it back to `http://` — the
error says so.

**A newly generated configuration does not enforce.** Its authorizer slot is
empty, on purpose; authorization is adopted per deployment. Fill it.

---

## What still has to be typed more than once

On every host but the CA's, the CA's URL goes into every system's file, because
the template writes *this host*. That is the last per-system edit, and the fix
is small: a per-host defaults file the template reads before falling back to
this host — one edit per host, none per system. It is not written yet, and
this document will lose a row from its table when it is.

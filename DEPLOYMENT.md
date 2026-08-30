# Deploying a local cloud

This is the technician's document. [SECURITY.md](SECURITY.md) says what each
control is for; [`shells/`](https://github.com/sdoque/shells) holds the scripts;
this says what to do, in what order, on which machine — and what it looks like
when it has gone wrong.

**Status: the two-host section was run for real on 30 August 2026** — aiko
(10.0.0.33) as the first host, a fresh Debian 13 Pi as the second — and
corrected against what happened. The three-host shape is still the plan.

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

Two things a fresh Debian does not have, and the scripts assume: **`tmux`**
(`sudo apt install tmux` — it needs a password, so it is the technician's
step, not a script's) and **the scripts themselves**, which live in `shells/`
and not in `rpiExec/`. Copy `start_systems.sh` and `stop_systems.sh` beside
the binaries.

Then copy `rpiExec/` — or run `download_systems.sh` from `shells/`, then copy
the whitelist into `ca/` by hand, since the downloader cannot ship it.

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
| the cloud's name (`localcloud`) | the registrar's file on every host, all agreeing | it defaults to the host's name, which is right for one host and wrong for the second |

**The cloud's name lives in the registrars, and only there.** Membership is
decided where the election is: a registrar whose `/status` names another
cloud is not a peer, is refused with one line in the log, and is not asked
again. So two hosts whose registrars disagree do not quietly form one cloud —
the second host's registrar takes its own lead and its systems join nothing
you meant. Set the name on every host's registrar before the second host
starts. And name it for *where it is*, not what it is for: two clouds both
called by the default of some demo collide the moment their graphs meet in
one store.

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

What was done, and what happened. The second host ran `maitreD`,
`serviceregistrar` and a `thermostat` whose sensor and valve were both on the
first host — chosen so that every line it draws has to cross.

1. On the CA host, add the new IP to `maitreDHosts`; restart the CA.
2. On the new host: `tmux`, the scripts, `rpiExec/`; `systems.txt` =
   `maitreD`, `serviceregistrar`, `thermostat`.
3. Start once to generate. Edit exactly the table's rows: the CA's URL in all
   three files, the authorizer's in all three, and the first host's registrar
   appended to the registrar's file. **Leave the orchestrator entry alone** —
   the template wrote this host, where no orchestrator runs, and that is the
   case the framework has to handle.
4. Start.

Within a minute of enrolling, each system logged what it had learned:

    maitreD: learned from the registry that orchestrator is at https://10.0.0.33:30103/…
    maitreD: learned from the registry that serviceregistrar is at http://10.0.0.33:20102/…

The new registrar answered `/status` with *On standby, leading registrar is
http://10.0.0.33:20102/…*. The thermostat, whose file named an orchestrator on
its own host, passed over it — *does not answer* — and reached the learned one:

    the temperature is 23.25 °C with an error -3.25 °C and valve set at 33.75%

a reading from a 1-wire sensor on host A, driving a servo on host A, from a
controller on host B that was never told where either was.

The first run of this found the framework returning the file's dead
orchestrator without looking further; that is fixed (`mbaigo b90a702`), and it
is why step 3 says to leave the entry alone — it is the test.

Expect the first five minutes to say *not in whitelist* — see below.

---

## A Windows host

**Status: the plan, written 30 August 2026 for a test the next day.** To be
corrected against it.

Attestation on Windows is, if anything, better than on Linux: the kernel
locks a running image, so the file the maitreD hashes cannot be changed under
a live process. The maitreD asks the kernel for the process's image path
(`QueryFullProcessImageName`) and needs no privilege for a process of the
same user; a system started with *Run as administrator* answers *access
denied* and is refused, exactly as a `sudo`-started one is on Linux.

1. **Build machine**: `make win && make whitelist`. `win` builds the portable
   systems (`PORTABLE` in the Makefile — maitreD, registrar, thermostat,
   painter, envoy, kgrapher, modeler, collector; add to it on the command
   line) as `<system>_win64.exe`, and the whitelist hashes every platform's
   binaries together: one release, one CA.
2. **CA host**: add the Windows machine's IP to `maitreDHosts`; restart the CA.
3. **Windows machine**: copy `rpiExec\` and, from `shells\`,
   `start_systems.ps1` and `stop_systems.ps1`. No tmux — each system gets its
   own console window. `systems.txt` = `maitreD`, `esr`, then its systems.
   Run once to generate configurations, edit the table's rows, run again.
4. **The firewall will ask.** The first time each `.exe` listens, Windows
   Defender prompts to allow it on the network — say yes for *private*
   networks, or nothing on the Pis can reach it. The one that matters most
   is the maitreD's port 20101: the CA connects **inbound** to it to attest,
   and a blocked prompt looks like *not in whitelist*.
5. **Same user, not elevated.** Start everything from one ordinary PowerShell.

`stop_systems.ps1` stops processes without a signal, so systems stopped that
way do not unregister; their records lapse within a period. Ctrl-C in a
window is the graceful way.

## A Mac

`make mac` builds the same set as `<system>_mac64`, natively (the maitreD
needs cgo there). Tested 30 August 2026: a Mac maitreD enrolled with a Pi's
CA, attested an `envoy` on the same laptop, and the canvas served at
`http://127.0.0.1:8191/` with no tunnel — the same path a Windows host takes. Read what its attestation means before relying on it: macOS
gives the maitreD a *path*, not the running image, so a binary replaced after
it started would attest as its replacement. That is why a Mac is an
administrative host — `envoy` on the laptop — and not a controller.

## When the lead registrar goes away

Tested on 30 August 2026 by stopping aiko's registrar with two hosts running.

- **The standby takes the lead within a second** of the lead stopping to
  answer, and every system finds it on its next five-second tick — the file's
  entry refuses, the learned one leads.
- **A standby's registry is empty.** It does not replicate the lead's records,
  on purpose: a registry that can disagree with itself is worse than one that
  is briefly empty and says so. Systems re-register **within seconds** of
  noticing the lead has moved — they used to wait out their registration
  period, up to two minutes for a controller — and until each has, a *new*
  discovery of its services finds nothing. Existing bindings do not notice:
  the thermostat on the second host went on reading and driving throughout.
- **A renewal carries the old lead's id.** The new lead may hold that number
  for another service; it registers the renewal afresh rather than refusing
  it. Before that fix every system took a 500 and waited out a full period.
- **A standby refuses to be read, and says where the lead is.** It used to
  answer a query with an empty list, and every core system that had cached
  the old lead's address went on asking it and believing it: the authorizer
  refused every request in the cloud for thirteen minutes while the system it
  was refusing was registered and running. Now a standby answers every read
  and write with the referral, and the orchestrator and authorizer forget a
  registrar that refuses them.
- **When the old lead returns it stands by**, because the election consults
  the registrars it has learned and not only the ones its file names. Before
  that fix a first host's registrar — whose file named no peer — retook the
  lead two milliseconds after starting, and every system re-registered a
  second time.

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

**You cannot see a process you know is running.** `pgrep _rpi64` does not
list `thermostat_rpi64`: Linux keeps fifteen characters of a process name and
`thermostat_rpi6` is what is left. Use `pgrep -f`, and anchor it —
`pgrep -f '^\./thermostat_rpi64'` — or it matches your own shell and kills
your session when you `kill` what it found. Starting a second instance on the
strength of the first being invisible gives you two, sharing one log.

**A newly generated configuration does not enforce.** Its authorizer slot is
empty, on purpose; authorization is adopted per deployment. Fill it.

---

## What still has to be typed more than once

On every host but the CA's, the CA's URL goes into every system's file, because
the template writes *this host*. That is the last per-system edit, and the fix
is small: a per-host defaults file the template reads before falling back to
this host — one edit per host, none per system. It is not written yet, and
this document will lose a row from its table when it is.

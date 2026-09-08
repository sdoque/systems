# cartographer

Consumes sweeps from a [guetteur](../guetteur) and builds an occupancy map,
working out as it goes where each sweep was taken from.

It maps and it localizes; it does not drive. It knows nothing about motors,
wheel radius or the articulation angle — if a `pose` service exists it uses it
as a prior and is much the better for it, and if none exists it matches each
sweep against the map built so far.

## Services

| service | form | what it is |
|---|---|---|
| `map` | `MapA_v1a` | the occupancy grid built so far |
| `pose` | `PoseA_v1a` | where in the map the sensor is believed to be |
| `coverage` | `SignalA_v1a` | the share of the grid that has been observed at all |

A `map.pgm` is also written to disk every ten seconds. That is the artefact a
person looks at; the `map` service is what a system consumes.

## What it is, and what it is not

This is a SLAM **front end**. There is no pose graph and no loop closure, so a
long circuit of a building will not snap shut when it returns to where it
started — the drift accumulated on the way round simply stays. For a hallway
driven up and back, which is the experiment the students are being given, that
is the right amount of machinery.

## Three things that are not obvious, and were each found by a failing test

**1. Walls must be recorded as surfaces, not points.** A sweep samples a wall at
intervals that grow with range, so recording only beam endpoints leaves a wall
as a row of dots. A later sweep then scores best by dropping its endpoints back
into those same dots — which is the pose the vehicle has already left. The
matcher fits the sampling pattern instead of the geometry and reports that
nothing has moved. `integrate` therefore joins adjacent returns that are within
30 cm of each other, and the range jump at a doorway is what stops it drawing a
wall across the opening.

**2. Matching scores against a distance transform, not the occupancy grid.**
A Euclidean distance field makes a wall uniformly attractive along its length,
so the along-wall direction carries no false evidence.

**3. A straight corridor cannot say how far along it you are.** Two parallel
walls look identical from everywhere between them. This is the aperture problem
and it is a property of the geometry, not a defect to be tuned away.

So the matcher probes its own answer: it nudges the winning pose and asks
whether the score actually falls. A flat peak is reported as flat, and then:

- **with odometry**, the odometry is taken as the pose and the matcher is not
  allowed to slide it along the direction the map is indifferent to;
- **without odometry**, the sweep is *not* integrated at all, and the system
  says so. A corridor mapped from a pose that cannot be known comes out the
  wrong length and looks entirely convincing.

## Odometry

Every motor on the mini wheel loader has an encoder, so odometry is available in
hardware — but nothing publishes it yet. The `loader` writes setpoints and reads
nothing back, and *commanded* speed is not measured speed: a stalled or slipping
wheel reports the motion that was asked for rather than the motion that
happened, which is the worst possible input to a mapping system.

What is needed is the loader publishing measured wheel speeds and the waist
angle, and a driver system turning those into a pose with the vehicle's
geometry. This system consumes a `pose` service and does not care which system
provides it.

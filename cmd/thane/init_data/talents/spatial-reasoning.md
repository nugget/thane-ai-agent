# Spatial Reasoning

How to think about physical space, presence, and location in a smart home.

## Hierarchy of Place

Homes have nested spatial concepts:

1. **Property** — The entire premises (may include multiple buildings)
2. **Building** — House, garage, workshop, guest house
3. **Floor** — Ground floor, upstairs, basement
4. **Room** — Kitchen, bedroom, office
5. **Zone** — Functional areas within rooms (reading nook, workbench)

When discussing location, use the most specific level that's meaningful. "In the office" beats "in the house" when precision helps.

## Presence vs Location

**Presence** answers: "Is someone here?" (binary: home/away)
**Location** answers: "Where exactly?" (room-level granularity)

These serve different purposes:
- Presence triggers: "Welcome home" automations, arm/disarm security
- Location enables: "Turn on lights where I am", "play music in this room"

## Arrival and Departure are Two Events

Coming and going are distinct moments worth tracking:
- **Arrival**: New context begins (greetings, status updates, comfort adjustments)
- **Departure**: Context ends (security modes, energy saving, notifications)

The gap between them is *dwell time* — how long someone spent somewhere.

## Multiple Occupants

A home often has multiple people. Consider:
- **Who** is present matters for personalization
- **Everyone away** vs **someone home** changes security posture
- Individual preferences may conflict (temperature, lighting)
- Privacy: not everyone wants their location tracked with equal granularity

## Room Context Enriches Commands

"Turn on the light" means different things depending on where you are. When location is known, use it:
- Assume commands target the current room unless specified
- Offer clarification if ambiguous: "The office light, or did you mean the hallway?"

## Zones Beyond Home

Presence extends beyond the property:
- Work, gym, frequently visited places
- Travel (airports, hotels)
- "Away" isn't uniform — being at work vs being on vacation implies different things

## Examples

Good: "You arrived home at 17:30 and went to the office."
Bad: "device_tracker.phone changed to home at 17:30:00."

Good: "No one is in the living room — should I turn off the TV?"
Bad: "binary_sensor.living_room_occupancy is off."

Good: "Dan is in the stable, Monica is away."
Bad: "person.dan state: Stable. person.monica state: not_home."

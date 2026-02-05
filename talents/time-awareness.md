# Time Awareness

How to think about and communicate time in a smart home context.

## Core Principles

**Context determines format.** The same timestamp needs different treatment depending on use:
- **Recent events** (< 24h): Time only ("14:30")
- **This week**: Include day ("Tuesday 14:30") 
- **Further out**: Include date ("Feb 15, 14:30")
- **Historical/logs**: ISO 8601 ("2026-02-05T14:30:00")

**Use 24-hour time** unless the user explicitly prefers 12-hour. It's unambiguous and fits smart home contexts (schedules, automations, logs).

**Include weekday for planning.** When discussing future events, appointments, or schedules, the day of week helps humans orient: "Your dentist appointment is Thursday at 09:00" beats "2026-02-13 09:00".

**Relative time for immediacy.** "5 minutes ago", "in 2 hours" often communicates better than exact timestamps for recent/near events.

## Smart Home Specifics

**Sunrise/sunset are meaningful anchors.** "Turn on lights at sunset" or "motion detected 20 minutes after sunrise" connects time to the physical environment.

**Schedules think in local time.** Always use the home's timezone for automation discussions, even if internal systems use UTC.

**Event sequences matter.** When reporting what happened: "Motion detected at 14:30, lights turned on at 14:30:02, turned off at 14:45" — the sequence tells the story.

## Examples

Good: "The front door opened at 17:23, about 10 minutes ago."
Bad: "The front door opened at 2026-02-05T17:23:45.123Z."

Good: "Your heating schedule runs weekdays at 06:30."
Bad: "Your heating schedule runs at 06:30:00."

Good: "The garage was last accessed Tuesday at 11:15."
Bad: "The garage was last accessed 2026-02-04T11:15:00-06:00."

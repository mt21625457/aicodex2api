## ADDED Requirements
### Requirement: Sticky-session hit reuses schedulable accounts list
The scheduler SHALL resolve sticky-session account selection from the schedulable accounts list already loaded for the request and SHALL NOT issue an additional account-by-ID database query when the sticky account is present in that list.

#### Scenario: Sticky session hit without extra DB query
- **WHEN** a scheduling request has a sticky session that points to an account in the schedulable accounts list
- **THEN** the scheduler reuses that in-memory account data and does not query the database by account ID

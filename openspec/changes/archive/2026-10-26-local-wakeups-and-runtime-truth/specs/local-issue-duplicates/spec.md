## ADDED Requirements
### Requirement: Duplicate relationships remain local and one level deep
The SQLite runtime SHALL implement the upstream duplicate relation read and issue-update contract. Marking an issue SHALL require a different original in the same workspace, cancel the duplicate, reject cycles and chains and incompatible writes, and preserve the original. Reopening a duplicate SHALL clear the mark, and deleting an original SHALL clear its remaining pointers transactionally without foreign keys or cascade constraints.
#### Scenario: Mark and restart
- **WHEN** a user marks one local issue as a duplicate of another and restarts Desktop
- **THEN** the cancelled duplicate and both relation directions remain visible through the real existing UI
#### Scenario: Invalid graph or removed original
- **WHEN** an update would create a chain/cycle or the original is deleted
- **THEN** the update is rejected without partial changes, or the deletion clears dangling marks while preserving other issues

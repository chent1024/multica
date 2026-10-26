## 1. Implement local persistence and contracts
- [x] 1.1 Add additive migrations and preservation/rollback tests.
- [x] 1.2 Implement all upstream Wakeup management, list, summary, history and system-rule endpoints with boundary validation.
- [x] 1.3 Implement durable time/event/condition execution, expiry, pause limits, atomic task dispatch and restart recovery.
- [x] 1.4 Connect existing local issue/task/stage events and system child-done behavior.
- [x] 1.5 Implement and verify duplicate marking/read/reopen/delete cleanup.
- [x] 1.6 Implement workflow CRUD/editor/project bindings/status mapping and real handler entry.
## 2. Correct runtime truth
- [x] 2.1 Preserve unavailable providers through running-daemon IPC.
- [x] 2.2 Persist actual daemon CLI metadata separately from provider version and test legacy rows.
## 3. Validate and deliver locally
- [x] 3.1 Run affected real tests, race checks and typecheck.
- [x] 3.2 Complete specs, validate/archive this change in the same candidate, and perform rv with scoped fixes.
- [x] 3.3 Backup current app/profile, verify snapshot migration, build and install the exact reviewed source.
- [x] 3.4 Perform native final acceptance and verify preserved data; leave changes uncommitted and unpushed.

# Changelog

## [0.3.1](https://github.com/kunchenguid/ezoss/compare/ezoss-v0.3.0...ezoss-v0.3.1) (2026-05-05)


### Bug Fixes

* **ghclient:** ignore numeric actor ids ([#31](https://github.com/kunchenguid/ezoss/issues/31)) ([7d72cd2](https://github.com/kunchenguid/ezoss/commit/7d72cd2ccb1eb62a967736f0b2800123ba263a83))

## [0.3.0](https://github.com/kunchenguid/ezoss/compare/ezoss-v0.2.2...ezoss-v0.3.0) (2026-05-04)


### Features

* add coding agent fix jobs ([#20](https://github.com/kunchenguid/ezoss/issues/20)) ([557f3ad](https://github.com/kunchenguid/ezoss/commit/557f3adf7eb93830e96fa6da540d1e4a37a33b05))
* add contributor mode ([#25](https://github.com/kunchenguid/ezoss/issues/25)) ([686129b](https://github.com/kunchenguid/ezoss/commit/686129bcf1b69e7c3d55ae33dfad884b734726f8))
* **cli:** queue approved fixes on approval ([#28](https://github.com/kunchenguid/ezoss/issues/28)) ([876217b](https://github.com/kunchenguid/ezoss/commit/876217bd1a62a4d26179697e23d1afb9b77f3353))
* **tui:** add guided rerun instructions ([#18](https://github.com/kunchenguid/ezoss/issues/18)) ([aadbe69](https://github.com/kunchenguid/ezoss/commit/aadbe69486e0f48f2f87ba3928dae68b5c2074b1))
* **tui:** open inbox items in browser ([#22](https://github.com/kunchenguid/ezoss/issues/22)) ([7e29dd3](https://github.com/kunchenguid/ezoss/commit/7e29dd3350f0feb56072e7e4809c0f62be00cf6a))


### Bug Fixes

* **daemon:** requeue triaged items after new activity ([#26](https://github.com/kunchenguid/ezoss/issues/26)) ([ebbacde](https://github.com/kunchenguid/ezoss/commit/ebbacde11d665380e8c0c372084f989d9d86e19b))
* **db:** supersede cancellable fix jobs ([#27](https://github.com/kunchenguid/ezoss/issues/27)) ([7c04180](https://github.com/kunchenguid/ezoss/commit/7c04180fadfebeb1b9a59917898a6f40597b8640))
* **shellenv:** detach shell probes from terminal ([#29](https://github.com/kunchenguid/ezoss/issues/29)) ([8f0b4dc](https://github.com/kunchenguid/ezoss/commit/8f0b4dcdd79e4b0f65241746458672cba26ea43b))
* **tui:** extend selected row highlight fill ([#24](https://github.com/kunchenguid/ezoss/issues/24)) ([4a52272](https://github.com/kunchenguid/ezoss/commit/4a5227260db0cc64f7bbe7984685ed98b8ceaf50))
* **tui:** keep selection stable after actions ([#21](https://github.com/kunchenguid/ezoss/issues/21)) ([b15eee1](https://github.com/kunchenguid/ezoss/commit/b15eee13787db8dbe3aedd69652c73390d79958b))

## [0.2.2](https://github.com/kunchenguid/ezoss/compare/ezoss-v0.2.1...ezoss-v0.2.2) (2026-04-29)


### Bug Fixes

* release workflow upload ([7cc105a](https://github.com/kunchenguid/ezoss/commit/7cc105a0553737a215d71f5f42734eac1fb1c0d8))

## [0.2.1](https://github.com/kunchenguid/ezoss/compare/ezoss-v0.2.0...ezoss-v0.2.1) (2026-04-29)


### Bug Fixes

* failing test in workflow_test.go ([6f3d709](https://github.com/kunchenguid/ezoss/commit/6f3d70941ff4bd718248d45c2334a6a8f43b8934))

## [0.2.0](https://github.com/kunchenguid/ezoss/compare/ezoss-v0.1.0...ezoss-v0.2.0) (2026-04-29)


### Features

* **cli:** add interactive status TUI ([#5](https://github.com/kunchenguid/ezoss/issues/5)) ([37285d7](https://github.com/kunchenguid/ezoss/commit/37285d73f8591ca0d7a9041b8f67f401918893a3))
* **cli:** add managed checkouts for triage ([#3](https://github.com/kunchenguid/ezoss/issues/3)) ([bf5bb84](https://github.com/kunchenguid/ezoss/commit/bf5bb846efdd292815184bb9512fa93a5ed8b560))
* **daemon:** add structured lifecycle logging ([#11](https://github.com/kunchenguid/ezoss/issues/11)) ([909a724](https://github.com/kunchenguid/ezoss/commit/909a724f359600c1d63de5aa06fd0ae60b5ccc08))
* initial commit ([df6f7f4](https://github.com/kunchenguid/ezoss/commit/df6f7f4fe7bf94951bcc38cfce19b1b727dc185d))
* **triage:** add coding-agent fix prompts ([#7](https://github.com/kunchenguid/ezoss/issues/7)) ([b3891e0](https://github.com/kunchenguid/ezoss/commit/b3891e05c2e207b31a28e24a7e0ce248ba349953))
* **triage:** require acknowledgement option in prompt ([d4a603a](https://github.com/kunchenguid/ezoss/commit/d4a603a618214da8270ab35f5db522982a9d8cd8))
* **tui:** add focused card scrolling ([#4](https://github.com/kunchenguid/ezoss/issues/4)) ([d00a04e](https://github.com/kunchenguid/ezoss/commit/d00a04efe66dec5dc829dc38a0a6b994329be34f))
* **tui:** clarify mark triaged action ([#14](https://github.com/kunchenguid/ezoss/issues/14)) ([5622ab2](https://github.com/kunchenguid/ezoss/commit/5622ab22890f57f844e94f24963d31985253ba37))


### Bug Fixes

* **cli:** handle daemon readiness before inbox startup ([#13](https://github.com/kunchenguid/ezoss/issues/13)) ([4e23aec](https://github.com/kunchenguid/ezoss/commit/4e23aecd4b1e4c06af3b246f9ce166e1a5def671))
* **daemon:** keep polling after transient errors ([#12](https://github.com/kunchenguid/ezoss/issues/12)) ([f670aca](https://github.com/kunchenguid/ezoss/commit/f670aca188ddd035be3fd8e0dcd53557de0edfd3))

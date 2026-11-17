# Changelog

## [0.2.2](https://github.com/basisbit/karaoke-feeds/compare/v0.2.1...v0.2.2) (2026-11-17)


### Bug fixes

* accept only http(s) mirrors from a feed ([ca9b69c](https://github.com/basisbit/karaoke-feeds/commit/ca9b69c41977c4fe0a9b514aacd504bc40d30a22))
* count resumed bytes once when the server ignores Range ([f91e447](https://github.com/basisbit/karaoke-feeds/commit/f91e447368f3d166acdc86d008f0ab78bc5a6c00))
* keep a returning song hidden until its files are complete ([ec1ceb1](https://github.com/basisbit/karaoke-feeds/commit/ec1ceb1f0884b8a9d9888438c90dda989a5a1c2e))
* release the sync lock after a crash ([5e33d96](https://github.com/basisbit/karaoke-feeds/commit/5e33d96eb31d378e7647bc5d658da2b2bfed428f))
* report Fetch progress from one goroutine at a time ([41bfe4b](https://github.com/basisbit/karaoke-feeds/commit/41bfe4bea505b576490e7281050b938970c5d770))
* store feed locations only after all checks ([8faef9c](https://github.com/basisbit/karaoke-feeds/commit/8faef9cda9e343eed54809fe3efbefd11a7b0d6b))

## [0.2.1](https://github.com/basisbit/karaoke-feeds/compare/v0.2.0...v0.2.1) (2026-11-16)


### CI

* show all commit types in release notes ([2200f02](https://github.com/basisbit/karaoke-feeds/commit/2200f023135fef3d3e3d15aee6524383b05edf02))

## [0.2.0](https://github.com/basisbit/karaoke-feeds/compare/v0.1.1...v0.2.0) (2026-11-15)


### ⚠ BREAKING CHANGES

* add pack kinds to the feed format

### Features

* add pack kinds to the feed format ([0ac0672](https://github.com/basisbit/karaoke-feeds/commit/0ac0672252f7845f17f0b1e5aec7c433c522ba4c))
* range requests support + easier integration of kfeed into other apps ([8a011db](https://github.com/basisbit/karaoke-feeds/commit/8a011db9064039873a105c6872dfa4353bad6fd2))


### Bug fixes

* item conflicts, cleanup and target checks from review ([d49822d](https://github.com/basisbit/karaoke-feeds/commit/d49822d382648deb930a44deb3b28b692707d887))

## [0.1.1](https://github.com/basisbit/karaoke-feeds/compare/v0.1.0...v0.1.1) (2026-11-14)


### Bug fixes

* **serve:** serve only the files of a feed ([ea225c9](https://github.com/basisbit/karaoke-feeds/commit/ea225c9da878b3c32462a390431d6bb8939941fa))

## 0.1.0 (2026-11-14)


### Features

* karaoke feed format draft 0.2 and the kfeed tool ([579f22e](https://github.com/basisbit/karaoke-feeds/commit/579f22e213874f90cb0848258a9e7afc7608e246))

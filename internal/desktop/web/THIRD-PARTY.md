# Third-party files in the desktop app's page

The page loads nothing from the network. These files ship inside the app,
unchanged from the packages named below. Each one's license sits next to it.

| File | From | Version | License |
| --- | --- | --- | --- |
| `vendor/marked.esm.js` | npm `marked`, `lib/marked.esm.js` | 18.0.14 | MIT (`vendor/LICENSE.marked`) |
| `vendor/purify.es.mjs` | npm `dompurify`, `dist/purify.es.mjs` | 3.4.16 | Apache-2.0 or MPL-2.0 (`vendor/LICENSE.dompurify`) |
| `fonts/ibm-plex-sans-latin-{400,500,600}-normal.woff2`, `fonts/ibm-plex-sans-latin-400-italic.woff2` | npm `@fontsource/ibm-plex-sans`, `files/` | 5.3.0 | SIL Open Font License 1.1 (`fonts/LICENSE.ibm-plex-sans`) |
| `fonts/ibm-plex-mono-latin-{400,500}-normal.woff2` | npm `@fontsource/ibm-plex-mono`, `files/` | 5.3.0 | SIL Open Font License 1.1 (`fonts/LICENSE.ibm-plex-mono`) |
| `fonts/newsreader-latin-{400,500}-normal.woff2` | npm `@fontsource/newsreader`, `files/` | 5.3.0 | SIL Open Font License 1.1 (`fonts/LICENSE.newsreader`) |

The fonts cover the Latin character set only. Text in other scripts falls
back to the system's fonts.

## Checksums

SHA-256 of each file as copied from its package:

```text
528a1b88bef88fc27277e06036ce7f4afb6a220b18110ba57c09e292b24a7ce0  vendor/marked.esm.js
c44274a7959cfdd4da871fa78a5d5fbbef55db68d118c5c0833bc4b5cf9633ad  vendor/purify.es.mjs
08949f728dc52d528e69b1667d15c89a5686a4ee9a296ff90983985f99c380f7  fonts/ibm-plex-mono-latin-400-normal.woff2
01d285447409c8a588692162439a038b8cbd7871309ee20267b0d2d91c6e8e22  fonts/ibm-plex-mono-latin-500-normal.woff2
6de912e531b6c98084f1b2d5e5a91bad77be4e68bc4e396e43c46fc435e5f3d9  fonts/ibm-plex-sans-latin-400-italic.woff2
3b646991d30055a93a4ecc499713d4347953a74a947ecab435ab72070cbdab0e  fonts/ibm-plex-sans-latin-400-normal.woff2
0717336fb31fcdcde4b8deb3675bb4a0f7f6d484864afcd6751ac29975962203  fonts/ibm-plex-sans-latin-500-normal.woff2
8960851d691c054ed38e259bdcf1a6190d157b4203ed5bb32c632a863fb8ec2f  fonts/ibm-plex-sans-latin-600-normal.woff2
e66067814f1c672d33a457e4f4d102c818b481420e2234cf685ebdbf2f443904  fonts/newsreader-latin-400-normal.woff2
5613e2fc8377392c02e8ac9d55014689fb5320a5f2a7be55e8088a314728ac2c  fonts/newsreader-latin-500-normal.woff2
```

## Updating one

Download the package's tarball from the npm registry with `curl`, copy the
same file over the old one, and update the version and checksum here. The
page imports each library by file name, so nothing else changes. No `npm`
install is needed.

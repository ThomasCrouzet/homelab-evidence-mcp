# Third-party components

Dependencies are declared in `go.mod` and locked by `go.sum`.

## Direct dependencies

| Module | Usage |
|---|---|
| `github.com/modelcontextprotocol/go-sdk` | MCP client and server protocol |
| `gopkg.in/yaml.v3` | configuration decoding |

The MCP SDK is distributed under an Apache-2.0/MIT combination and `yaml.v3`
under an MIT/Apache combination. Indirect dependencies are listed in `go.mod`.

Each release ships `LICENSE.txt` and `THIRD_PARTY_LICENSES.txt`, generated from
the real module graph incorporated into the binary and containing every license
or notice file found at the root of each module.

This project is distributed under the MIT license. See [LICENSE](LICENSE).

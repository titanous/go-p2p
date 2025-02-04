# P2P
A Library for writing Peer-to-Peer applications.

It seems most peer-to-peer libraries go to great lengths to provide a reliable stream abstraction over as many different transports as possible.
This makes them unsuitable for building protocols over lossy mediums (UDP, IP, ethernet).

It is also the wrong abstraction for the job.  Almost no protocols are actually stream based.
Reliable streams are just used to create reliable request-response abstractions in most cases.
Many protocols are built on reliable request-response abstractions.
It is a valuable primitive to have.

This library takes a message based approach instead of a stream based approach.
Messages can either be unreliable, or reliable and (therefore) require a response.
These two kinds of communication are represented by the `Tell` and `Ask` methods respectively.

## Design
The core abstraction is the `Swarm`. Which represents a group of nodes which can send messages to one another.

```
type Swarm interface {
    Tell(addr Addr, data []byte) error
    ServeTells(TellHandler) error

    ParseAddr(data []byte) (Addr, error)
    LocalAddrs() []Addr
    MTU(Addr) int
    Close() error
}
```
`Addr` is an interface type.

Swarms each define their own Addr type, and should panic if a caller tries to send a message to an address of another type.
In other languages Swarms might be defined as a generic type.
```
type Swarm[A: Addr] {
    Tell(addr: A, data: []byte) error
    LocalAddrs() []A
    ...
}
```

Overlay networks are a common pattern in p2p systems.
Swarms have methods for introspection such as `LocalAddrs` and `MTU`.
`Addrs` also have a canonical serialization provided by `MarshalText` and `ParseAddr`
These two features make Swarms the building blocks for higher order swarms, which can be overlay networks.
The underlying swarm is often referred to as the `transport` throughout these packages.

The power of the `Swarm` abstraction is exemplified by the fact we merely call some `Swarms` "transports" in certain contexts rather than having a separate Transport type, as other libraries do.

`Swarms` provide a `Tell` method, which makes a best effort to send the message payload to the destination.
It will error if the message cannot be set in flight, but does not guarantee the transmision of the message if the error is nil.

The interface ends up resembling a hybrid between the server in `http` and the `PacketConn` interface from `net`

## Directory Organization 

### C is for Cell
Compare-and-swap cells are a synchronization primitive.
They are less general than message passing (`Tell` and `Ask`).
Message passing enables "push" communication, while cells only allow "pull" communication.
A cell can be implemented on top of message-passing, but message-passing can only be approximated with a cell (by polling).

Cells are useful for modeling shared state that can change.

- **HTTP Cell**
A cell implementation backed by an HTTP server.

- **NaCl Cell**
A cell which encrypts and signs its contents.
A secret key must be shared to decrypt the cell contents.

### D is for Discovery
Services for discovering peers.

- **Cell Tracker**
A tracker for announcing and looking up peer addresses.
Works on top of any cell.
A default client and server implementation are provided.

### S is for Swarm

- **Fragmenting Swarm**
A higher order swarm which increases the MTU of an underlying swarm by breaking apart messages,
and assembling them on the other side.

- **In-Memory Swarm**
A swarm which transfers data to other swarms in memory. Useful for testing.

- **Multi Swarm**
Creates a multiplexed addressed space using names given to each subswarm.
Applications can use this to "future-proof" their transport layer.

- **Noise Swarm**
A secure higher order swarm.
It secures messages using the Noise Protocol Framework's NN handshake.
It can run on top of any other swarm.

- **Peer Swarm**
A swarm that uses PeerIDs as addresses.
It requires an underlying swarm, and a function that maps PeerIDs to addresses.

- **QUIC Swarm**
A secure swarm supporting `Asks` built on the QUIC protocol (UDP based).

- **SSH Swarm**
A secure swarm supporting `Asks` built on the SSH protocol (TCP based).

- **UDP Swarm**
An insecure swarm, included mainly as a building block.

- **UPnP Swarm**
Creates and manages NAT mappings for addresses behind a IPv4 with a NAT table, using UPnP.
Applies the mappings to values returned from `LocalAddr`

The `swarmutil` package contains utilities for writing `Swarms` and a test suite to make sure it exhibits all the behaviors expected.

### P is for Protocols

The utility of this library is determined entirely by how easily well-known p2p algorithms can be built and composed using it's primitives.

- **Kademlia**
A package with a DHT, overlay network, and cache is in the works.  Right now a cache that evicts keys distant in XOR space is available.

- **Integer Multiplexing**
This is the simplest possible multiplexing scheme, it does not support asking, and prepends an integer, encoded as a varint to the message.

- **String Multiplexing**
Another very simple multiplexing scheme.  It does not support asking.  It prepends a length prefixed string to the message.

- **Dynamic Multiplexing**
A service which multiplexes multiple logical swarms, over the same underlying transport swarm.
Each multiplexed swarm is identified by a string, and a registry of multiplexed swarms is used to map each to an integer.
This makes for a good application platform, as it is possible to add and remove services.  Only services with the same name will be able to send messages to one another.

## PKI
A `PeerID` type is provided to be used as the hash of public keys, for identifying peers.
Canonical serialization functions are provided for public keys (just `x509.MarshalPKIXPublicKey`).

The `Sign` and `Verify` methods provided allow for keys to sign in multiple protocols without the risk of signature collisions.

## Test Utilities
The `p2ptest` packages contains utilities for testing, such as generating adjacency matricies for networks of various connectivities.

# Setting up Indexer Pool with Assigner Service

## Deploy Indexers

Deploy as many indexers as needed for current estimated index data and desired redundancy, and [configure](https://pkg.go.dev/github.com/ipni/storetheindex/config#Discovery) the indexers to use an Assigner Service, by setting the `UseAssigner` value to `true`. Nothing else needs to change from a stand-alone indexer deployment. As always, the indexers’ admin server should be available on a private network that is not externally accessible.

## Deploy Assigner Service

Deploy a single AS that is configured, in its configuration file, with an indexer [pool](https://pkg.go.dev/github.com/ipni/storetheindex@v0.5.7/assigner/config#Assignment) that has each [indexer’s information](https://pkg.go.dev/github.com/ipni/storetheindex@v0.5.7/assigner/config#Indexer). The assigner service should be able to receive advertisement announce messages from advertisement publishers, over gossip pub-sub and/or HTTP. If the AS is expected to relay direct HTTP announce messages, then configure the pool indexers as peers in the [peering](https://pkg.go.dev/github.com/ipni/storetheindex@v0.5.7/assigner/config#Config) section of the AS configuration, to allow the gossipsub messages to propagate across the pool. The AS is available as a sub-command of golang indexer implementation, `storetheindex`. 

## Add Indexers as Needed

As the amount of stored index data increases, the storage capacity of the indexers can be increased, or the number of indexers can be increased. For every indexer that is expected to become frozen, at least one additional indexer should be added to the indexer pool, before the indexer freezes, in order to continue indexing handed off from a frozen indexer.

Adding an indexer to the pool is done by deploying a new indexer configured to use an AS. Then configure that indexer’s information in the AS configuration and restart the AS.

## Example Assigner Service Configuration

Most of the configuration is generated by using the `storetheindex assigner init` command, which creates a JSON file containing a default assigner configuration. The example below populates the default configuration to show how the indexer pool is specified. Note, when used with public networks, set `FilterIPs` to `true` so that when publishers include non-routable addresses in their information, those addresses are ignored.

```json
{                                                                                                                                  
  "Version": 1,
  "Identity": {
    "PeerID": "12D3KooWNCqfNFu8psCGTcM6E4njMa9B9t42WTasJba4LLiqnVXL",
    "PrivKey": "<redacted>"
  },
  "Assignment": {
    "FilterIPs": true,
    "PollInterval": "30s",
    "IndexerPool": [
      {
        "AdminURL": "http://indexer-0:3002",
        "FindURL": "http://indexer-0:3000",
        "IngestURL": "http://indexer-0:3001"
      },
      {
        "AdminURL": "http://indexer-1:3002",
        "FindURL": "http://indexer-1:3000",
        "IngestURL": "http://indexer-1:3001"
      }
    ],
    "Policy": {
      "Allow": true,
      "Except": null
    },
    "PubSubTopic": "/indexer/ingest/mainnet",
    "PresetReplication": 1,
    "Replication": 1
  },
  "Bootstrap": {
    "Peers": [
      "/dns4/bootstrap-4.mainnet.filops.net/tcp/1347/p2p/12D3KooWL6PsFNPhYftrJzGgF5U18hFoaVhfGk7xwzD8yVrHJ3Uc",
      "/dns4/bootstrap-5.mainnet.filops.net/tcp/1347/p2p/12D3KooWLFynvDQiUpXoHroV1YxKHhPJgysQGH2k3ZGwtWzR4dFH"
    ],
    "MinimumPeers": 1
  },
  "Daemon": {
    "HTTPAddr": "/ip4/0.0.0.0/tcp/3701",
    "P2PAddr": "/ip4/0.0.0.0/tcp/3703",
    "NoResourceManager": false
  },
  "Logging": {
    "Level": "info",
    "Loggers": {
      "basichost": "warn",
      "bootstrap": "warn"
    }
  },
  "Peering": {
    "Peers": [
      "/dns4/indexer-0/tcp/3003/p2p/12D3KooWC7kMRLFT2kqv5XkhD7cR2Nw2UbKE5rwWk11VCZ22undU",
      "/dns4/indexer-1/tcp/3003/p2p/12D3KooWB31nfbhC4NPoLBujGS7LTBAbnhSaSF6ZViv5ip1g14Ax"
    ]
  }
}
```

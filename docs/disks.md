# Disks

Standalone disks hold data that outlives VMs. They live on a storage server and nodes attach them to VMs over NBD, so a disk can move between VMs and nodes. The storage server is optional and runs as a compose profile (an nbdkit server plus an agent that connects to the hub), usually on the hub host:

```sh
docker compose -f deploy/compose.yaml --profile storage up -d --build
```

The compose `storage` service reads the hub token from `/data/hub.json` on every connect, so a token changed in Settings is picked up after a reconnect. To run the storage server on another host, build and run it there with the token from Settings (update it there after changing the token):

```sh
docker build -f deploy/storage.Dockerfile -t vm-manager-storage .
docker run -d --restart unless-stopped -p 10809:10809 -v vm-manager-disks:/disks \
  -e HUB_TOKEN=<hub token> \
  vm-manager-storage -hub ws://<hub>:<HUB_PORT>/api/storage/connect
```

Nodes reach the storage server at the storage endpoint from Settings: by default the client endpoint with `STORAGE_PORT`, which fits the compose service on the hub host. For a storage server on another host set it to that host's `host:port`. Keep the `/disks` volume: the storage server never recreates the file of a disk it already had, so a lost volume shows those disks as `missing` instead of handing VMs blank disks.

Without a storage server, disks stay `creating` and VMs with disks fail to start with `storage not ready`. A new disk is usable once the storage server has created its file (`ready`); a VM attached earlier starts then. Create a disk with `POST /api/disks` or the GUI (**Disks** page), then attach it at VM creation (`disks` on `POST /api/vms`) or later:

```sh
curl -X POST http://<hub>:8080/api/disks -d '{"name": "data", "sizeGB": 10}'
# → {"id": "<disk-id>", ...}
curl -X PUT 'http://<hub>:8080/api/vms/<vm-id>/disks?restartAllowed=true' -d '{"disks": [{"diskId": "<disk-id>", "mountPath": "/data"}]}'
```

A disk is attached to at most one VM at a time, and mounted at `mountPath` (owned by `ubuntu` after the first format). Disk names are lowercase letters, digits and dashes. System paths such as `/etc`, `/usr`, `/home/ubuntu` or `/var` are rejected as mount paths.

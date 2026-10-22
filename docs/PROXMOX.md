# Running the PDP-11/70 appliance on Proxmox VE

Three ways, in order of preference:

1. **Native install in an unprivileged LXC container** (recommended): the
   lightest, starts in seconds, needs no special container features.
2. **A VM**, with `install.sh` or with Docker inside it: the most isolated.
3. **Docker inside an LXC container**: possible, but it needs extra container
   features and has storage pitfalls; Proxmox itself recommends a VM for
   application containers. Prefer 1 or 2.

What the appliance needs: one CPU core (the simulator runs on one host thread;
an idle guest in Fast mode costs about 5 % of a core, a busy one a full core),
memory for pdp11d (about 20 MB; up to about 230 MB while it unpacks a system,
which it returns afterwards, so 512 MB is enough) and the simulator (about
20 MB), and disk for the systems you install (2.11BSD: 1 GB sparse image,
about 170 MB used at first). No device passthrough, no privileges, no kernel
modules.

## 1. Unprivileged Debian 13 container with install.sh

### Create the container

In the web UI: download the Debian 13 template (node → local storage →
CT Templates → Templates → `debian-13-standard`), then **Create CT**:

| Setting | Value |
|---|---|
| Unprivileged container | yes (the default) |
| Nesting | the default (on) is fine; pdp11d does not need it |
| Template | `debian-13-standard_13.*_amd64.tar.zst` |
| Disk | 4 GB (8 GB if you install several systems) |
| Cores | 1 (2 make the one-time build faster) |
| Memory / Swap | 512 MB / 512 MB (1 GB memory if the container runs other things too) |
| Network | DHCP or a static address on your LAN bridge |
| Timezone (Options, after creation) | "use host settings" or your zone; the template's default is UTC |

Or on the host's shell (the template name comes from `pveam available`):

```sh
pveam update
pveam available --section system | grep debian-13
pveam download local debian-13-standard_13.1-2_amd64.tar.zst     # the name listed above
pct create 1170 local:vztmpl/debian-13-standard_13.1-2_amd64.tar.zst \
    --hostname pdp11 --unprivileged 1 --features nesting=1 \
    --cores 1 --memory 512 --swap 512 --rootfs local-lvm:4 \
    --net0 name=eth0,bridge=vmbr0,ip=dhcp --onboot 1 --startup down=120 \
    --timezone host
pct start 1170
```

The time zone matters to the guests that get the date and time typed in at
boot (RT-11, RSX, RSTS, UNIX V7): they take the container's local time. For an
existing container: `pct set 1170 --timezone host` and a restart of the
container, or `timedatectl set-timezone Europe/Zurich` inside it and
`systemctl restart pdp11d`.

`--startup down=120` (Options → Start/Shutdown order → Shutdown timeout in the
UI) gives the container 120 s to shut down when the host does: pdp11d first
shuts the guest OS down cleanly (2.11BSD needs about 16 s; the service allows
up to 90 s).

### Install

```sh
pct enter 1170                 # a root shell in the container
apt-get update && apt-get install -y git
git clone <this repository> pdp11-emu && cd pdp11-emu
./install.sh                   # about a minute; prints the URL
```

`install.sh` builds SimH and pdp11d, installs them to `/opt/pdp11`, creates
the user `pdp11` and the service `pdp11d.service` and starts it; 2.11BSD then
boots by itself. Open the printed URL (`http://<container address>:8080/`).

Set a password unless only you can reach the container:

```sh
nano /etc/pdp11/pdp11d.env      # PDP11_PASSWORD=...
systemctl restart pdp11d
```

The service works in an unprivileged container because its unit uses no
mount-namespace sandboxing (`ProtectSystem=`, `PrivateTmp=` and friends would
fail with status 226/NAMESPACE there). `systemctl status pdp11d` and
`journalctl -u pdp11d -f` show what it does.

Upgrade later with `cd pdp11-emu && git pull && ./install.sh` (it restarts the
service; the guest OS is shut down cleanly first). The build tools stay
installed for that; `./install.sh --uninstall` removes the appliance but keeps
its data and settings, `--uninstall --purge` removes everything.

## 2. A VM

Any Debian 12/13 or Ubuntu 22.04/24.04 VM works: 1 vCPU, 1 GB memory, 8 GB
disk. Inside it either run `install.sh` exactly as above, or install Docker
and use the image:

```sh
git clone <this repository> pdp11-emu && cd pdp11-emu
docker compose up -d
```

Docker needs Engine 23 or newer with BuildKit (buildx) and Compose v2.
Debian 12's own `docker.io` (20.10) and `docker-compose` (1.29) are too old:
install `docker-ce`, `docker-buildx-plugin` and `docker-compose-plugin` from
Docker's repository. Debian 13: `apt install docker.io docker-buildx
docker-compose`; Ubuntu 22.04/24.04: `apt install docker.io docker-buildx
docker-compose-v2`. Upgrade later with `git pull && docker compose up -d
--build` (the guest OS is shut down cleanly first; the disks stay in the
volume). Set the VM's time zone (`timedatectl set-timezone Europe/Zurich`) or
`TZ` in `.env` for the boot dialogues.

Give the VM a shutdown timeout of at least 120 s as well (VM → Options →
Start/Shutdown order), and enable the QEMU guest agent or ACPI shutdown so
that the host's shutdown reaches the services.

## 3. Docker inside an LXC container (not recommended)

Docker needs the container features `nesting=1` and `keyctl=1`
(`pct set 1170 --features nesting=1,keyctl=1`, then restart the container).
Known pitfalls: on ZFS-backed storage, Docker's overlay storage driver needs
OpenZFS 2.2 or newer (Proxmox VE 8.1+), otherwise Docker falls back to the slow
and space-hungry `vfs` driver; AppArmor and nested cgroups occasionally break
after Proxmox or Docker upgrades. With those in place, `docker compose up -d`
works as anywhere else. The native install of section 1 avoids all of this.

## Firewall

pdp11d listens on TCP 8080 (HTTP and WebSocket). With the Proxmox firewall
enabled for the container (CT → Firewall), allow 8080 only from your LAN (or
only from the reverse proxy). The Proxmox firewall filters outside the guest,
so it also covers Docker in a VM, whose published ports bypass a ufw or
firewalld inside the VM. If you enable the telnet terminal lines
(`PDP11_TTY_TELNET`, e.g. `:4000`), remember that they are unauthenticated and
that 2.11BSD's root has no password: allow that port only from trusted hosts.
Nothing else needs to be reachable; the simulator's console port
(`PDP11_PORT_BASE`, 23400) listens on localhost only.

## Reverse proxy

For HTTPS or a host name, put a proxy (nginx, Caddy, Traefik; often a separate
container) in front of `http://<container>:8080/`. It must forward WebSocket
upgrades and should keep the `Host` header with its port (nginx:
`proxy_set_header Host $http_host`, not `$host`, which drops a port other
than 443); for a sub-path such as `/pdp11/` it sends `X-Forwarded-Prefix:
/pdp11`. If the proxy rewrites `Host`, list the public host name in
`PDP11_ALLOW_ORIGINS`; without a password, list it in `PDP11_HOSTS`. Set
`PDP11_TRUSTED_PROXIES` to the proxy's address, so that the failed-login limit
counts the clients (from `X-Forwarded-For`) rather than the proxy. Example
configurations: README.md, "Behind a reverse proxy".

## Backups

Everything that changes lives in the data directory, `/var/lib/pdp11` (with
Docker: the volume mounted at `/data`): the disks of the installed systems
(sparse files), `panel.json` and `systems.json`. The settings are in
`/etc/pdp11/pdp11d.env`. A disk image copied while the guest OS runs is like
a pulled plug when restored (2.11BSD repairs it with fsck), so back up a
stopped system:

* **vzdump in stop mode** (Datacenter → Backup → Mode: Stop): Proxmox shuts
  the container down, which stops pdp11d, which shuts the guest OS down
  cleanly first; the container starts again after the backup. Snapshot mode
  copies a running guest.
* **By hand**:

  ```sh
  systemctl stop pdp11d             # clean guest shutdown, then power off
  tar -C /var/lib -cSzf /root/pdp11-data.tar.gz pdp11     # -S keeps the files sparse
  systemctl start pdp11d
  ```

  Restore with `systemctl stop pdp11d`, `tar -C /var/lib -xzf
  pdp11-data.tar.gz`, `chown -R pdp11:pdp11 /var/lib/pdp11`, `systemctl start
  pdp11d`. "Shut down OS" in the UI also stops the guest cleanly (and powers
  the machine off) without stopping the service.
* **Docker**: `docker compose stop`, then copy the volume, for example
  `docker run --rm -v pdp11emu_pdp11-data:/data:ro -v "$PWD":/backup
  debian:trixie-slim tar -C /data -cSzf /backup/pdp11-data.tar.gz .`, then
  `docker compose start`.

A system's disks can always be recreated from the original archive with
"Factory reset" in the system chooser; back up what you changed on them.

sudo apt update
sudo apt install btrfs-progs

sudo vi /etc/sudoers.d/yesnas
dj ALL=(root) NOPASSWD: /usr/bin/lsblk, /usr/sbin/smartctl, /usr/sbin/mdadm, /usr/sbin/wipefs, /usr/sbin/blkid, /usr/bin/mount, /usr/bin/umount, /usr/bin/tee, /usr/sbin/mkfs.btrfs, /usr/bin/btrfs, /usr/bin/mkdir, /usr/bin/rm, /usr/bin/dd, /bin/dd, /usr/bin/sync, /bin/sync, /usr/bin/cp, /bin/cp, /usr/bin/chmod, /bin/chmod, /usr/bin/touch, /bin/touch, /usr/bin/mv, /bin/mv, /usr/bin/testparm, /usr/bin/smbpasswd



[share]
   path = /srv/samba/share
   browseable = yes
   writable = yes
   guest ok = no
   read only = no
   valid users = dj
   create mask = 0664
   directory mask = 0775

   vfs objects = catia fruit streams_xattr
   fruit:metadata = stream
   fruit:resource = stream
   fruit:veto_appledouble = yes
   delete veto files = yes

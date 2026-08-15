sudo apt update
sudo apt install btrfs-progs

sudo vi /etc/sudoers.d/yesnas
dj ALL=(root) NOPASSWD: /usr/bin/lsblk, /usr/sbin/smartctl, /usr/sbin/mdadm, /usr/sbin/wipefs, /usr/sbin/blkid, /usr/bin/mount, /usr/bin/umount, /usr/bin/tee, /usr/sbin/mkfs.btrfs, /usr/bin/btrfs, /usr/bin/mkdir, /bin/mkdir, /usr/bin/rm, /bin/rm, /usr/bin/dd, /bin/dd, /usr/bin/sync, /bin/sync, /usr/bin/cp, /bin/cp, /usr/bin/chmod, /bin/chmod, /usr/bin/chown, /bin/chown, /usr/bin/touch, /bin/touch, /usr/bin/mv, /bin/mv, /usr/bin/testparm, /usr/bin/smbpasswd, /usr/bin/systemctl, /usr/bin/setfacl, /usr/bin/getfacl, /usr/sbin/exportfs, /usr/sbin/showmount, /usr/sbin/proftpd, /usr/sbin/apache2ctl, /usr/bin/htpasswd, /usr/bin/fusermount, /usr/bin/fusermount3, /usr/bin/id, /usr/sbin/dmidecode, /usr/bin/docker



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

SMB / FTP / WebDAV / NFS

sudo apt update
sudo apt install -y samba nfs-kernel-server proftpd-basic apache2 apache2-utils acl avahi-daemon
sudo a2enmod dav dav_fs auth_basic alias headers ssl
sudo systemctl restart apache2
sudo apt install -y openssl

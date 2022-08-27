#! /bin/bash

cd `dirname $0`

echo $HOSTNAME

if [[ "$HOSTNAME" == isucon1 ]]; then
  INSTANCE_NUM="1"
elif [[ "$HOSTNAME" == isucon2 ]]; then
  INSTANCE_NUM="2"
elif [[ "$HOSTNAME" == isucon3 ]]; then
  INSTANCE_NUM="3"
elif [[ "$HOSTNAME" == isucon4 ]]; then
  INSTANCE_NUM="4"
elif [[ "$HOSTNAME" == isucon5 ]]; then
  INSTANCE_NUM="5"
else
  echo "Invalid host"
  exit 1
fi

set -ex

git pull

sudo systemctl daemon-reload

# NGINX

if [[ "$INSTANCE_NUM" == 1 ]]; then
  sudo install -o root -g root -m 644 ./conf/etc/nginx/sites-available/isuconquest.conf /etc/nginx/sites-available/isuconquest.conf
  sudo install -o root -g root -m 644 ./conf/etc/nginx/nginx.conf /etc/nginx/nginx.conf
  sudo nginx -t

  sudo systemctl restart nginx
  sudo systemctl enable nginx
fi

if [[ "$INSTANCE_NUM" != 1 ]]; then
  sudo systemctl stop nginx.service
  sudo systemctl disable nginx.service
fi

# APP
if [[ "$INSTANCE_NUM" == 1 ]]; then
  pushd go
  /home/isucon/local/golang/bin/go build -o isuconquest
  popd
  sudo systemctl restart isuconquest.go.service
  sudo systemctl enable isuconquest.go.service
  
  sleep 2
  
  sudo systemctl status isuconquest.go.service --no-pager
fi

if [[ "$INSTANCE_NUM" != 1 ]]; then
  sudo systemctl disable --now isuconquest.go.service
fi

# MYSQL
if [[ "$INSTANCE_NUM" == 2 ]]; then
  sudo install -o root -g root -m 644 ./conf/etc/mysql/mysql.conf.d/mysqld.cnf /etc/mysql/mysql.conf.d/mysqld.cnf

  echo "MySQL restart したいなら手動でやってね"
#  sudo systemctl restart mysql
  sudo systemctl enable --now mysql
fi

if [[ "$INSTANCE_NUM" != 2 ]]; then
  sudo systemctl disable --now mysql.service
fi

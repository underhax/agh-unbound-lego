#=======================================================================#
# AGH Web Domain Template                                               #
# DO NOT MODIFY THIS FILE! CHANGES WILL BE LOST WHEN REBUILDING DOMAINS #
#=======================================================================#

server {
    listen      %ip%:80;

    server_name %domain_idn% *.%domain_idn%;

    root /dev/null;

    access_log  /var/log/nginx/domains/%domain%.log combined;
    access_log  /var/log/nginx/domains/%domain%.bytes bytes;
    error_log   /var/log/nginx/domains/%domain%.error.log error;

    location / {
       return 301 https://$host$request_uri;
    }

}

server {
    listen      %ip%:%web_ssl_port% ssl;
    include %home%/%user%/conf/web/%domain%/include_ipv6[.]conf;

    server_name %domain_idn% *.%domain_idn%;

    root        %sdocroot%;
    index       index.php index.html index.htm;
    access_log  /var/log/nginx/domains/%domain%.log combined;
    access_log  /var/log/nginx/domains/%domain%.bytes bytes;
    error_log   /var/log/nginx/domains/%domain%.error.log error;

    ssl_certificate      %home%/%user%/data/lego/certificates/%domain%.crt;
    ssl_certificate_key  %home%/%user%/data/lego/certificates/%domain%.key;
    ssl_trusted_certificate %home%/%user%/data/lego/certificates/%domain%.issuer.crt;

    # TLS 1.3 0-RTT anti-replay
    if ($anti_replay = 307) { return 307 https://$host$request_uri; }
    if ($anti_replay = 425) { return 425; }

    add_header Strict-Transport-Security "max-age=63072000" always;

    # Baseline security headers to prevent clickjacking and MIME-type sniffing
    add_header X-Content-Type-Options "nosniff" always;
    add_header X-Frame-Options "SAMEORIGIN" always;

    location = /favicon.ico {
        log_not_found off;
        access_log off;
    }

    location = /robots.txt {
        allow all;
        log_not_found off;
        access_log off;
    }

    location / {
        location ~* ^.+\.(jpeg|jpg|png|webp|gif|bmp|ico|svg|css|js)$ {
            expires     max;
            fastcgi_hide_header "Set-Cookie";
        }
    }

    location ~ [^/]\.php(/|$) {
        types { } default_type "text/html";
    }

    location /error/ {
        alias   %home%/%user%/web/%domain%/document_errors/;
    }

    location ~ /\.(?!well-known\/) {
       deny all;
       return 404;
    }

    location /vstats/ {
        alias   %home%/%user%/web/%domain%/stats/;
        include %home%/%user%/web/%domain%/stats/auth.conf*;
    }

    # Restricted Area
    location ~* ^/$ {
        auth_basic "Restricted Area";
        auth_basic_user_file %home%/%user%/.htpasswd;
    }

    location /dns-query {
        proxy_set_header Host $http_host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_redirect off;
        proxy_buffering on;
        proxy_http_version 1.1;
        proxy_read_timeout     6s;
        proxy_connect_timeout  6s;
        proxy_pass https://127.0.0.1:6443/dns-query;

        access_log off;
    }

    # Proxy to the AGH admin panel. Replace /agh-secret-path/ with a site-specific
    # unpredictable path. Cookie path rewrite is required because AGH sets cookies
    # scoped to '/' — without it, session cookies will not be sent back through the proxy prefix.
    # access_log is disabled to avoid leaking admin session activity into shared web logs.
    location /agh-secret-path/ {
        proxy_cookie_path / /agh-secret-path/;
        proxy_pass http://127.0.0.1:8002/;
        proxy_redirect / /agh-secret-path/;
        proxy_set_header Host $host;

        access_log off;
    }
}

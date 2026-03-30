#=======================================================================#
# AGH Web Domain Template                                               #
# DO NOT MODIFY THIS FILE! CHANGES WILL BE LOST WHEN REBUILDING DOMAINS #
#=======================================================================#

# Defines a shared memory zone for rate limiting.
# - $binary_remote_addr: Uses client's IP address as the key (efficient, binary format)
# - zone=agh_login_limit:10m: Stores state for up to 10MB of IPs (~160k unique IPs)
# - rate=10r/s: Allows 10 requests per second per IP (sufficient for normal admin usage)
limit_req_zone $binary_remote_addr zone=agh_login_limit:10m rate=10r/s;

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

    add_header X-Content-Type-Options "nosniff" always;
    add_header X-Frame-Options "SAMEORIGIN" always;
    add_header Content-Security-Policy "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:;" always;
    add_header Referrer-Policy "no-referrer" always;

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
        proxy_set_header X-Forwarded-For $remote_addr;
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
        # IP-based access restriction (uncomment and configure).
        # Multiple 'allow' directives are supported - each IP on a new line.
        # Requests from non-listed IPs will be denied.
        # Example:
        # allow 192.168.1.1;
        # allow 10.0.0.50;
        # deny all;

        # Rate limiting prevents brute-force attacks on admin login.
        # - zone=agh_login_limit: References the shared zone defined above
        # - burst=30: Allows bursts up to 30 requests (AGH admin main page ~16 requests,
        #   plus margin for HTTP/2/HTTP3 multiplexing and normal navigation)
        # - delay=25: Queues excess requests instead of dropping; 25 processed immediately,
        #   remaining 5 queued - prevents false positives that could trigger fail2ban
        # - To drop excess immediately instead of queuing, use 'nodelay' instead of 'delay'
        limit_req zone=agh_login_limit burst=30 delay=25;

        proxy_cookie_path / /agh-secret-path/;
        proxy_pass http://127.0.0.1:8002/;
        proxy_redirect / /agh-secret-path/;
        proxy_set_header Host $host;

        access_log off;
    }
}

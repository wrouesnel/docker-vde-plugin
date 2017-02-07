# Start multiple HTTP servers

Simple package to allow easily starting multiple HTTP/HTTPS listener services.

Servers are started by specifying addresses with URL-like schemas:
	* unix:///var/run/server.socket : open a Unix socket file on /var/run/server
	* tcp://0.0.0.0:80 : listen on tcp port 80.
	
It supports both HTTP and HTTPS servers, and allows specifying different
certificate packages for each HTTPS listener.
FROM binwiederhier/ntfy

# Persistent data (cache, auth, attachments) lives on the mounted volume.
RUN mkdir -p /var/lib/ntfy

EXPOSE 80

ENTRYPOINT ["ntfy"]
CMD ["serve"]

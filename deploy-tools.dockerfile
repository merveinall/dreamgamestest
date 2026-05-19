FROM alpine:3.20

ENV ANSIBLE_HOST_KEY_CHECKING=False
ENV ANSIBLE_COLLECTIONS_PATH=/usr/share/ansible/collections

RUN apk add --no-cache \
    bash \
    curl \
    openssh-client \
    python3 \
    py3-pip \
    py3-kubernetes \
    ansible \
    kubectl

RUN mkdir -p /usr/share/ansible/collections && \
    ansible-galaxy collection install kubernetes.core -p /usr/share/ansible/collections

CMD ["cat"]

def outer():
    def inner():
        pass
    return inner

def target():
    pass

struct Logger;
enum Event {
    Click(Logger),
    Move { target: Logger },
    Quit,
}

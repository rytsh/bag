class Child extends Worker {
    void start() {}
    void run(Worker worker) {
        this.start();
        super.stop();
        worker.start();
    }
}

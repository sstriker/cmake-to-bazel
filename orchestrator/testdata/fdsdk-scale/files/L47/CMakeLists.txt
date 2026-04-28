cmake_minimum_required(VERSION 3.20)
project(L47 LANGUAGES C VERSION 0.1.0)

find_package(L32 CONFIG REQUIRED)
find_package(L37 CONFIG REQUIRED)
find_package(L39 CONFIG REQUIRED)

add_library(L47 STATIC src/L47.c)
target_include_directories(L47 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L47 PUBLIC L32::L32 L37::L37 L39::L39)

install(TARGETS L47
        EXPORT L47Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L47Targets
        FILE L47Targets.cmake
        NAMESPACE L47::
        DESTINATION lib/cmake/L47)
